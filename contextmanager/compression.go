package contextmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// CompressionStrategy 是一次摘要策略记录的稳定标识。
type CompressionStrategy struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (s CompressionStrategy) Validate() error {
	if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Version) == "" {
		return fmt.Errorf("compression strategy name and version are required")
	}
	if s.Name != strings.TrimSpace(s.Name) || s.Version != strings.TrimSpace(s.Version) {
		return fmt.Errorf("compression strategy name and version must be canonical")
	}
	return nil
}

// CompressionGroup 是不可分割、有序的历史单元。
// Core 按对话轮次构建分组，并合并 Tool Call/Result 关系。
type CompressionGroup struct {
	StartIndex int               `json:"start_index"`
	Messages   []message.Message `json:"messages"`
}

// CompressionTrigger explains why Core is requesting a structural summary.
type CompressionTrigger string

const (
	CompressionTriggerBudgetOverflow        CompressionTrigger = "budget_overflow"
	CompressionTriggerProviderContextLength CompressionTrigger = "provider_context_length"
)

func (g CompressionGroup) Clone() CompressionGroup {
	return CompressionGroup{StartIndex: g.StartIndex, Messages: message.CloneAll(g.Messages)}
}

// CompressionRequest contains only the inactive groups eligible for replacement.
type CompressionRequest struct {
	Model   model.Identity     `json:"model"`
	Plan    Plan               `json:"plan"`
	Trigger CompressionTrigger `json:"trigger,omitempty"`
	Groups  []CompressionGroup `json:"groups"`
}

func (r CompressionRequest) Clone() CompressionRequest {
	cloned := r
	cloned.Groups = make([]CompressionGroup, len(r.Groups))
	for index, group := range r.Groups {
		cloned.Groups[index] = group.Clone()
	}
	return cloned
}

func (r CompressionRequest) Validate() error {
	if err := r.Model.Validate(); err != nil {
		return err
	}
	if err := r.Plan.Validate(); err != nil {
		return fmt.Errorf("compression plan: %w", err)
	}
	if r.Plan.Model != r.Model {
		return fmt.Errorf("compression requires a matching plan")
	}
	switch r.effectiveTrigger() {
	case CompressionTriggerBudgetOverflow:
		if r.Plan.Fits || r.Plan.OverflowTokens <= 0 {
			return fmt.Errorf("budget compression requires an overflow plan")
		}
	case CompressionTriggerProviderContextLength:
		if !r.Plan.Fits || r.Plan.OverflowTokens != 0 {
			return fmt.Errorf("Provider context-length recovery requires a fitting preflight plan")
		}
	default:
		return fmt.Errorf("unsupported compression trigger %q", r.Trigger)
	}
	if len(r.Groups) == 0 {
		return fmt.Errorf("compression has no eligible history groups")
	}
	nextIndex := r.Groups[0].StartIndex
	seen := make(map[string]struct{})
	for groupIndex, group := range r.Groups {
		if group.StartIndex != nextIndex || len(group.Messages) == 0 {
			return fmt.Errorf("compression group %d is not contiguous", groupIndex)
		}
		for messageIndex, value := range group.Messages {
			if err := value.Validate(); err != nil {
				return fmt.Errorf("compression group %d message %d: %w", groupIndex, messageIndex, err)
			}
			if _, exists := seen[value.ID]; exists {
				return fmt.Errorf("duplicate compression message id %q", value.ID)
			}
			seen[value.ID] = struct{}{}
		}
		nextIndex += len(group.Messages)
	}
	return nil
}

func (r CompressionRequest) effectiveTrigger() CompressionTrigger {
	if r.Trigger == "" {
		return CompressionTriggerBudgetOverflow
	}
	return r.Trigger
}

// CompressionDecision summarizes a positive prefix of eligible groups.
type CompressionDecision struct {
	Strategy      CompressionStrategy `json:"strategy"`
	CoveredGroups int                 `json:"covered_groups"`
	Summary       string              `json:"summary"`
}

func (d CompressionDecision) Validate(request CompressionRequest) error {
	if err := d.Strategy.Validate(); err != nil {
		return err
	}
	if d.CoveredGroups <= 0 || d.CoveredGroups > len(request.Groups) {
		return fmt.Errorf("covered groups must be between 1 and %d", len(request.Groups))
	}
	if strings.TrimSpace(d.Summary) == "" {
		return fmt.Errorf("compression summary is required")
	}
	return nil
}

// Compressor produces summary text and coverage while Core owns transformation.
type Compressor interface {
	Compress(context.Context, CompressionRequest) (CompressionDecision, error)
}

// Compression is the Core-built request view and its source provenance.
type Compression struct {
	Messages          []message.Message   `json:"messages"`
	Strategy          CompressionStrategy `json:"strategy"`
	CoveredGroups     int                 `json:"covered_groups"`
	CoveredMessageIDs []string            `json:"covered_message_ids"`
	SummaryMessageID  string              `json:"summary_message_id"`
}

func (c Compression) Clone() Compression {
	cloned := c
	cloned.Messages = message.CloneAll(c.Messages)
	cloned.CoveredMessageIDs = append([]string(nil), c.CoveredMessageIDs...)
	return cloned
}

// NewCompressionRequest discovers only the contiguous, inactive history
// prefix that may be summarized. The latest turn, System messages, and any
// group containing an unresolved Tool Call remain protected.
func NewCompressionRequest(modelIdentity model.Identity, messages []message.Message, plan Plan) (CompressionRequest, error) {
	return newCompressionRequest(modelIdentity, messages, plan, CompressionTriggerBudgetOverflow)
}

// NewProviderContextLengthCompressionRequest creates one structural recovery
// request after the Provider rejects a request that passed local preflight.
func NewProviderContextLengthCompressionRequest(modelIdentity model.Identity, messages []message.Message, plan Plan) (CompressionRequest, error) {
	return newCompressionRequest(modelIdentity, messages, plan, CompressionTriggerProviderContextLength)
}

func newCompressionRequest(modelIdentity model.Identity, messages []message.Message, plan Plan, trigger CompressionTrigger) (CompressionRequest, error) {
	if err := validateConversation(messages); err != nil {
		return CompressionRequest{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	groups, err := compressionGroups(messages)
	if err != nil {
		return CompressionRequest{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	request := CompressionRequest{Model: modelIdentity, Plan: plan, Trigger: trigger, Groups: groups}
	if err := request.Validate(); err != nil {
		return CompressionRequest{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	return request.Clone(), nil
}

// ApplyCompression performs the replacement itself so a Compressor cannot
// reorder protected history or split a semantic group.
func ApplyCompression(messages []message.Message, request CompressionRequest, decision CompressionDecision, summaryMessageID string) (Compression, error) {
	if err := request.Validate(); err != nil {
		return Compression{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	if err := decision.Validate(request); err != nil {
		return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: err}
	}
	if err := validateConversation(messages); err != nil {
		return Compression{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	summaryMessageID = strings.TrimSpace(summaryMessageID)
	if summaryMessageID == "" {
		return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: fmt.Errorf("summary message id is required")}
	}
	for _, value := range messages {
		if value.ID == summaryMessageID {
			return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: fmt.Errorf("summary message id %q already exists", summaryMessageID)}
		}
	}
	coveredEnd := request.Groups[decision.CoveredGroups-1].StartIndex + len(request.Groups[decision.CoveredGroups-1].Messages)
	coveredStart := request.Groups[0].StartIndex
	if coveredStart < 0 || coveredEnd > len(messages) || coveredStart >= coveredEnd {
		return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: fmt.Errorf("compression coverage is outside the source history")}
	}
	coveredIDs := make([]string, 0, coveredEnd-coveredStart)
	for index := coveredStart; index < coveredEnd; index++ {
		coveredIDs = append(coveredIDs, messages[index].ID)
	}
	if err := coverageMatches(messages, request, coveredStart, coveredEnd); err != nil {
		return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: err}
	}
	metadata, err := json.Marshal(struct {
		Kind             string              `json:"kind"`
		Strategy         CompressionStrategy `json:"strategy"`
		SourceMessageIDs []string            `json:"source_message_ids"`
	}{Kind: "context_summary", Strategy: decision.Strategy, SourceMessageIDs: coveredIDs})
	if err != nil {
		return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: err}
	}
	summary := message.Message{
		ID: summaryMessageID, Role: message.RoleSystem, Visibility: message.VisibilityInternal,
		Content: []message.Content{{Type: message.ContentText, Text: decision.Summary}}, Metadata: metadata,
	}
	result := make([]message.Message, 0, len(messages)-(coveredEnd-coveredStart)+1)
	result = append(result, message.CloneAll(messages[:coveredStart])...)
	result = append(result, summary)
	result = append(result, message.CloneAll(messages[coveredEnd:])...)
	if err := validateConversation(result); err != nil {
		return Compression{}, &Error{Code: ErrorInvalidCompression, Cause: fmt.Errorf("compressed history: %w", err)}
	}
	return Compression{
		Messages: result, Strategy: decision.Strategy, CoveredGroups: decision.CoveredGroups,
		CoveredMessageIDs: coveredIDs, SummaryMessageID: summaryMessageID,
	}, nil
}

type historyGroup struct {
	start     int
	end       int
	protected bool
}

func compressionGroups(messages []message.Message) ([]CompressionGroup, error) {
	if len(messages) < 2 {
		return nil, fmt.Errorf("compression requires inactive history and an active message")
	}
	boundaries := make([]bool, len(messages))
	boundaries[0] = true
	for index := 1; index < len(messages); index++ {
		if messages[index].Role == message.RoleUser || messages[index].Role == message.RoleSystem {
			boundaries[index] = true
		}
	}
	calls := make(map[string]int)
	results := make(map[string]int)
	for messageIndex, value := range messages {
		for _, content := range value.Content {
			if content.ToolCall != nil {
				calls[content.ToolCall.ID] = messageIndex
			}
			if content.ToolResult != nil {
				results[content.ToolResult.CallID] = messageIndex
			}
		}
	}
	for callID, callIndex := range calls {
		resultIndex, resolved := results[callID]
		if resolved && callIndex > resultIndex {
			return nil, fmt.Errorf("tool result %q precedes its call", callID)
		}
		if resolved {
			for index := callIndex + 1; index <= resultIndex; index++ {
				boundaries[index] = false
			}
		}
	}
	starts := make([]int, 0, len(messages))
	for index, boundary := range boundaries {
		if boundary {
			starts = append(starts, index)
		}
	}
	groupForIndex := make([]int, len(messages))
	groups := make([]historyGroup, len(starts))
	for index, start := range starts {
		end := len(messages)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		groups[index] = historyGroup{start: start, end: end}
		for messageIndex := start; messageIndex < end; messageIndex++ {
			groupForIndex[messageIndex] = index
		}
	}
	for callID, callIndex := range calls {
		if _, resolved := results[callID]; !resolved {
			groups[groupForIndex[callIndex]].protected = true
		}
	}
	for groupIndex := range groups {
		for index := groups[groupIndex].start; index < groups[groupIndex].end; index++ {
			if messages[index].Role == message.RoleSystem {
				groups[groupIndex].protected = true
			}
		}
	}
	groups[len(groups)-1].protected = true
	firstEligible := 0
	for firstEligible < len(groups) && systemOnlyGroup(messages, groups[firstEligible]) {
		firstEligible++
	}
	firstProtected := len(groups)
	for index := firstEligible; index < len(groups); index++ {
		group := groups[index]
		if group.protected {
			firstProtected = index
			break
		}
	}
	if firstProtected == firstEligible {
		return nil, fmt.Errorf("compression has no inactive semantic group")
	}
	eligible := make([]CompressionGroup, firstProtected-firstEligible)
	for index, group := range groups[firstEligible:firstProtected] {
		eligible[index] = CompressionGroup{StartIndex: group.start, Messages: message.CloneAll(messages[group.start:group.end])}
	}
	return eligible, nil
}

func systemOnlyGroup(messages []message.Message, group historyGroup) bool {
	if group.start < 0 || group.start >= group.end {
		return false
	}
	for index := group.start; index < group.end; index++ {
		if messages[index].Role != message.RoleSystem {
			return false
		}
	}
	return true
}

func validateConversation(messages []message.Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("context messages are required")
	}
	messageIDs := make(map[string]struct{}, len(messages))
	calls := make(map[string]string)
	results := make(map[string]struct{})
	for index, value := range messages {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
		if _, exists := messageIDs[value.ID]; exists {
			return fmt.Errorf("duplicate message id %q", value.ID)
		}
		messageIDs[value.ID] = struct{}{}
		for _, content := range value.Content {
			if content.ToolCall != nil {
				if value.Role != message.RoleAssistant {
					return fmt.Errorf("tool call must belong to an assistant message")
				}
				if _, exists := calls[content.ToolCall.ID]; exists {
					return fmt.Errorf("duplicate tool call id %q", content.ToolCall.ID)
				}
				calls[content.ToolCall.ID] = content.ToolCall.Name
			}
			if content.ToolResult != nil {
				if value.Role != message.RoleTool {
					return fmt.Errorf("tool result must belong to a tool message")
				}
				name, exists := calls[content.ToolResult.CallID]
				if !exists || name != content.ToolResult.Name {
					return fmt.Errorf("tool result references an unknown or mismatched call %q", content.ToolResult.CallID)
				}
				if _, exists := results[content.ToolResult.CallID]; exists {
					return fmt.Errorf("duplicate tool result for call %q", content.ToolResult.CallID)
				}
				results[content.ToolResult.CallID] = struct{}{}
			}
		}
	}
	return nil
}

func coverageMatches(messages []message.Message, request CompressionRequest, start, end int) error {
	index := start
	for _, group := range request.Groups {
		for _, source := range group.Messages {
			if index >= end {
				return nil
			}
			if !reflect.DeepEqual(messages[index], source) {
				return fmt.Errorf("compression source history changed at message %d", index)
			}
			index++
		}
	}
	if index != end {
		return fmt.Errorf("compression coverage does not match source history")
	}
	return nil
}
