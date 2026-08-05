package compression

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

const (
	DefaultMaxSummaryBytes = 64 << 10
	maxCases               = 1_000
	maxFactsPerCase        = 1_000
	maxFactVariants        = 100
	maxFactTextBytes       = 4 << 10
)

// Config binds the same Planner and Compressor used by an application.
type Config struct {
	Planner    contextmanager.Planner
	Compressor contextmanager.Compressor
}

// Case is an overflowing request fixture and the facts expected in the
// generated summary. SourceMessageIDs make fact coverage auditable.
type Case struct {
	ID              string
	Request         contextmanager.Request
	RequiredFacts   []RequiredFact
	MaxSummaryBytes int
}

// RequiredFact accepts one of several deterministic textual representations.
// Matching is case-insensitive after collapsing whitespace; it is not a
// general semantic or hallucination judge.
type RequiredFact struct {
	ID               string
	SourceMessageIDs []string
	AnyOf            []string
}

// Evaluate executes each Case once. Invalid suite definitions return an error
// before Planner or Compressor delegates are called; delegate failures are
// recorded as failed Case results.
func Evaluate(ctx context.Context, config Config, cases []Case) (Report, error) {
	if config.Planner == nil || config.Compressor == nil {
		return Report{}, fmt.Errorf("compression evaluation requires Planner and Compressor")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := validateCases(cases)
	if err != nil {
		return Report{}, err
	}
	results := make([]CaseResult, len(normalized))
	for index, definition := range normalized {
		results[index] = evaluateCase(ctx, config, definition, index+1)
	}
	return Report{
		SchemaVersion: SchemaV1,
		GeneratedAt:   time.Now().UTC(),
		Summary:       summarize(results),
		Cases:         results,
	}, nil
}

func evaluateCase(ctx context.Context, config Config, definition Case, ordinal int) (result CaseResult) {
	started := time.Now()
	result = CaseResult{ID: definition.ID}
	problems := make([]string, 0, 7)
	defer func() {
		result.DurationMicros = time.Since(started).Microseconds()
	}()

	before, err := callPlanner(ctx, config.Planner, cloneRequest(definition.Request))
	if err != nil {
		result.FailureCode = "source_plan_failed"
		result.Failure = safeDelegateFailure("Planner", err)
		return result
	}
	if err := validatePlan(before, definition.Request); err != nil {
		result.FailureCode = "invalid_source_plan"
		result.Failure = "Planner returned an invalid source Plan."
		return result
	}
	result.BeforeInputTokens = before.InputTokens
	if before.Fits {
		result.FailureCode = "source_not_overflowing"
		result.Failure = "The source Request does not overflow."
		return result
	}
	request, err := contextmanager.NewCompressionRequest(definition.Request.Model, definition.Request.Messages, before)
	if err != nil {
		result.FailureCode = "compression_request_failed"
		result.Failure = "Core could not construct the Compression Request."
		return result
	}
	decision, err := callCompressor(ctx, config.Compressor, request.Clone())
	if err != nil {
		result.FailureCode = "compressor_failed"
		result.Failure = safeDelegateFailure("Compressor", err)
		return result
	}
	result.StrategyName = decision.Strategy.Name
	result.StrategyVersion = decision.Strategy.Version
	result.CoveredGroups = decision.CoveredGroups
	if err := decision.Validate(request); err != nil {
		result.FailureCode = "invalid_decision"
		result.Failure = "Compressor returned an invalid Decision."
		return result
	}
	result.Checks.DecisionValid = true

	summaryID := uniqueSummaryID(definition.Request.Messages, ordinal)
	compressed, err := contextmanager.ApplyCompression(definition.Request.Messages, request, decision, summaryID)
	if err != nil {
		result.FailureCode = "apply_failed"
		result.Failure = "Core rejected the Compression Decision."
		return result
	}
	result.Checks.StructuralIntegrity = true
	result.CoveredMessages = len(compressed.CoveredMessageIDs)

	result.Checks.ProtectedMessagesPreserved = protectedMessagesPreserved(definition.Request.Messages, compressed)
	if !result.Checks.ProtectedMessagesPreserved {
		problems = append(problems, "uncovered Messages changed")
	}
	result.Checks.ProvenanceExact = provenanceExact(compressed)
	if !result.Checks.ProvenanceExact {
		problems = append(problems, "summary provenance is inconsistent")
	}

	summaryText := summaryText(compressed)
	result.SummaryBytes = len(summaryText)
	result.Checks.SummaryBounded = result.SummaryBytes <= definition.MaxSummaryBytes
	if !result.Checks.SummaryBounded {
		problems = append(problems, fmt.Sprintf("summary bytes %d exceed %d", result.SummaryBytes, definition.MaxSummaryBytes))
	}
	result.Facts, result.Checks.RequiredFactsPreserved = evaluateFacts(definition.RequiredFacts, compressed.CoveredMessageIDs, summaryText)
	if !result.Checks.RequiredFactsPreserved {
		problems = append(problems, "required facts were not covered and matched")
	}

	afterRequest := cloneRequest(definition.Request)
	afterRequest.Messages = message.CloneAll(compressed.Messages)
	after, err := callPlanner(ctx, config.Planner, afterRequest)
	if err != nil {
		problems = append(problems, safeDelegateFailure("Planner", err))
	} else if err := validatePlan(after, afterRequest); err != nil {
		problems = append(problems, "Planner returned an invalid compressed Plan")
	} else {
		result.AfterInputTokens = after.InputTokens
		result.Checks.CompressedBudgetFits = after.Fits
		if !after.Fits {
			problems = append(problems, "compressed request still overflows")
		}
	}

	result.Passed = result.Checks.DecisionValid && result.Checks.StructuralIntegrity &&
		result.Checks.ProtectedMessagesPreserved && result.Checks.ProvenanceExact &&
		result.Checks.RequiredFactsPreserved && result.Checks.SummaryBounded &&
		result.Checks.CompressedBudgetFits
	if !result.Passed {
		result.FailureCode = "quality_checks_failed"
		result.Failure = strings.Join(problems, "; ")
	}
	return result
}

func validateCases(cases []Case) ([]Case, error) {
	if len(cases) == 0 || len(cases) > maxCases {
		return nil, fmt.Errorf("compression evaluation requires between 1 and %d Cases", maxCases)
	}
	normalized := make([]Case, len(cases))
	caseIDs := make(map[string]struct{}, len(cases))
	for index, definition := range cases {
		definition.ID = strings.TrimSpace(definition.ID)
		if definition.ID == "" {
			return nil, fmt.Errorf("Case %d requires an ID", index)
		}
		if _, exists := caseIDs[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate Case ID %q", definition.ID)
		}
		caseIDs[definition.ID] = struct{}{}
		if err := definition.Request.Validate(); err != nil {
			return nil, fmt.Errorf("Case %q Request: %w", definition.ID, err)
		}
		if err := validateFixtureConversation(definition.Request.Messages); err != nil {
			return nil, fmt.Errorf("Case %q conversation: %w", definition.ID, err)
		}
		if definition.MaxSummaryBytes == 0 {
			definition.MaxSummaryBytes = DefaultMaxSummaryBytes
		}
		if definition.MaxSummaryBytes < 1 || definition.MaxSummaryBytes > DefaultMaxSummaryBytes {
			return nil, fmt.Errorf("Case %q MaxSummaryBytes must be between 1 and %d", definition.ID, DefaultMaxSummaryBytes)
		}
		if len(definition.RequiredFacts) == 0 || len(definition.RequiredFacts) > maxFactsPerCase {
			return nil, fmt.Errorf("Case %q requires between 1 and %d facts", definition.ID, maxFactsPerCase)
		}
		messageIDs := make(map[string]struct{}, len(definition.Request.Messages))
		for _, value := range definition.Request.Messages {
			messageIDs[value.ID] = struct{}{}
		}
		factIDs := make(map[string]struct{}, len(definition.RequiredFacts))
		facts := make([]RequiredFact, len(definition.RequiredFacts))
		for factIndex, fact := range definition.RequiredFacts {
			fact.ID = strings.TrimSpace(fact.ID)
			if fact.ID == "" {
				return nil, fmt.Errorf("Case %q fact %d requires an ID", definition.ID, factIndex)
			}
			if _, exists := factIDs[fact.ID]; exists {
				return nil, fmt.Errorf("Case %q has duplicate fact ID %q", definition.ID, fact.ID)
			}
			factIDs[fact.ID] = struct{}{}
			if len(fact.SourceMessageIDs) == 0 {
				return nil, fmt.Errorf("Case %q fact %q requires SourceMessageIDs", definition.ID, fact.ID)
			}
			sources := make([]string, len(fact.SourceMessageIDs))
			seenSources := make(map[string]struct{}, len(sources))
			for sourceIndex, sourceID := range fact.SourceMessageIDs {
				sourceID = strings.TrimSpace(sourceID)
				if _, exists := messageIDs[sourceID]; !exists {
					return nil, fmt.Errorf("Case %q fact %q references unknown Message %q", definition.ID, fact.ID, sourceID)
				}
				if _, exists := seenSources[sourceID]; exists {
					return nil, fmt.Errorf("Case %q fact %q repeats source Message %q", definition.ID, fact.ID, sourceID)
				}
				seenSources[sourceID] = struct{}{}
				sources[sourceIndex] = sourceID
			}
			if len(fact.AnyOf) == 0 || len(fact.AnyOf) > maxFactVariants {
				return nil, fmt.Errorf("Case %q fact %q requires between 1 and %d variants", definition.ID, fact.ID, maxFactVariants)
			}
			variants := make([]string, len(fact.AnyOf))
			for variantIndex, variant := range fact.AnyOf {
				variant = normalizeText(variant)
				if variant == "" || len(variant) > maxFactTextBytes {
					return nil, fmt.Errorf("Case %q fact %q variant %d must contain 1 to %d bytes", definition.ID, fact.ID, variantIndex, maxFactTextBytes)
				}
				variants[variantIndex] = variant
			}
			facts[factIndex] = RequiredFact{ID: fact.ID, SourceMessageIDs: sources, AnyOf: variants}
		}
		definition.Request = cloneRequest(definition.Request)
		definition.RequiredFacts = facts
		normalized[index] = definition
	}
	return normalized, nil
}

func evaluateFacts(facts []RequiredFact, coveredIDs []string, summary string) ([]FactResult, bool) {
	covered := make(map[string]struct{}, len(coveredIDs))
	for _, id := range coveredIDs {
		covered[id] = struct{}{}
	}
	normalizedSummary := normalizeText(summary)
	results := make([]FactResult, len(facts))
	allPassed := true
	for index, fact := range facts {
		result := FactResult{ID: fact.ID, SourcesCovered: true}
		for _, sourceID := range fact.SourceMessageIDs {
			if _, exists := covered[sourceID]; !exists {
				result.SourcesCovered = false
				break
			}
		}
		for _, variant := range fact.AnyOf {
			if strings.Contains(normalizedSummary, variant) {
				result.Matched = true
				break
			}
		}
		result.Passed = result.SourcesCovered && result.Matched
		if !result.Passed {
			allPassed = false
		}
		results[index] = result
	}
	return results, allPassed
}

func protectedMessagesPreserved(source []message.Message, compressed contextmanager.Compression) bool {
	covered := make(map[string]struct{}, len(compressed.CoveredMessageIDs))
	for _, id := range compressed.CoveredMessageIDs {
		covered[id] = struct{}{}
	}
	expected := make([]message.Message, 0, len(source))
	for _, value := range source {
		if _, exists := covered[value.ID]; !exists {
			expected = append(expected, message.Clone(value))
		}
	}
	actual := make([]message.Message, 0, len(compressed.Messages))
	for _, value := range compressed.Messages {
		if value.ID != compressed.SummaryMessageID {
			actual = append(actual, message.Clone(value))
		}
	}
	return reflect.DeepEqual(expected, actual)
}

func provenanceExact(compressed contextmanager.Compression) bool {
	for _, value := range compressed.Messages {
		if value.ID != compressed.SummaryMessageID {
			continue
		}
		var metadata struct {
			Kind             string                             `json:"kind"`
			Strategy         contextmanager.CompressionStrategy `json:"strategy"`
			SourceMessageIDs []string                           `json:"source_message_ids"`
		}
		return json.Unmarshal(value.Metadata, &metadata) == nil && metadata.Kind == "context_summary" &&
			metadata.Strategy == compressed.Strategy && reflect.DeepEqual(metadata.SourceMessageIDs, compressed.CoveredMessageIDs)
	}
	return false
}

func summaryText(compressed contextmanager.Compression) string {
	for _, value := range compressed.Messages {
		if value.ID == compressed.SummaryMessageID && len(value.Content) == 1 && value.Content[0].Type == message.ContentText {
			return value.Content[0].Text
		}
	}
	return ""
}

func validatePlan(plan contextmanager.Plan, request contextmanager.Request) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Model != request.Model || plan.OutputReserveTokens != request.OutputReserveTokens {
		return fmt.Errorf("Plan changed Model or output reserve")
	}
	return nil
}

func cloneRequest(request contextmanager.Request) contextmanager.Request {
	cloned := request
	cloned.Messages = message.CloneAll(request.Messages)
	cloned.Tools = make([]model.ToolDefinition, len(request.Tools))
	for index, tool := range request.Tools {
		cloned.Tools[index] = tool.Clone()
	}
	return cloned
}

func uniqueSummaryID(messages []message.Message, ordinal int) string {
	existing := make(map[string]struct{}, len(messages))
	for _, value := range messages {
		existing[value.ID] = struct{}{}
	}
	for suffix := ordinal; ; suffix++ {
		candidate := fmt.Sprintf("compression_eval_summary_%d", suffix)
		if _, exists := existing[candidate]; !exists {
			return candidate
		}
	}
}

func normalizeText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func validateFixtureConversation(messages []message.Message) error {
	messageIDs := make(map[string]struct{}, len(messages))
	type call struct {
		name  string
		index int
	}
	calls := make(map[string]call)
	results := make(map[string]struct{})
	for messageIndex, value := range messages {
		if _, exists := messageIDs[value.ID]; exists {
			return fmt.Errorf("duplicate Message ID %q", value.ID)
		}
		messageIDs[value.ID] = struct{}{}
		for _, content := range value.Content {
			if content.ToolCall != nil {
				if _, exists := calls[content.ToolCall.ID]; exists {
					return fmt.Errorf("duplicate Tool Call ID %q", content.ToolCall.ID)
				}
				calls[content.ToolCall.ID] = call{name: content.ToolCall.Name, index: messageIndex}
			}
			if content.ToolResult != nil {
				definition, exists := calls[content.ToolResult.CallID]
				if !exists || definition.name != content.ToolResult.Name || definition.index > messageIndex {
					return fmt.Errorf("Tool Result references an unknown, later, or mismatched Call %q", content.ToolResult.CallID)
				}
				if _, exists := results[content.ToolResult.CallID]; exists {
					return fmt.Errorf("duplicate Tool Result for Call %q", content.ToolResult.CallID)
				}
				results[content.ToolResult.CallID] = struct{}{}
			}
		}
	}
	return nil
}

type delegatePanicError struct {
	boundary string
}

func (e *delegatePanicError) Error() string {
	return e.boundary + " panicked"
}

func safeDelegateFailure(boundary string, err error) string {
	var panicErr *delegatePanicError
	if errors.As(err, &panicErr) {
		return boundary + " panicked."
	}
	return boundary + " returned an error."
}

func callPlanner(ctx context.Context, planner contextmanager.Planner, request contextmanager.Request) (plan contextmanager.Plan, err error) {
	defer func() {
		if recover() != nil {
			err = &delegatePanicError{boundary: "Planner"}
		}
	}()
	return planner.Plan(ctx, request)
}

func callCompressor(ctx context.Context, compressor contextmanager.Compressor, request contextmanager.CompressionRequest) (decision contextmanager.CompressionDecision, err error) {
	defer func() {
		if recover() != nil {
			err = &delegatePanicError{boundary: "Compressor"}
		}
	}()
	return compressor.Compress(ctx, request)
}
