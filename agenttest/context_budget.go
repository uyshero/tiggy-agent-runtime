package agenttest

import (
	"fmt"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

// InstructionStackBudget defines optional byte ceilings for production-safe
// Context Manifest diagnostics. A zero ceiling means "not limited".
type InstructionStackBudget struct {
	MaxBytes             int64
	MaxSystemPromptBytes int64
	MaxProjectBytes      int64
	MaxTaskRuleBytes     int64
	MaxPolicyBytes       int64
	MaxKnowledgeBytes    int64
	MaxSkillBytes        int64
}

// ContextBudget defines optional ceilings for content-free model Context
// Manifest diagnostics. A zero ceiling means "not limited".
//
// Use this in tests to guard the shape of the model-visible request without
// logging or inspecting prompt text, Tool schemas, Memory content, or Tool
// results.
type ContextBudget struct {
	InstructionStack InstructionStackBudget

	MaxToolDefinitionBytes int64
	MaxToolArgumentBytes   int64
	MaxTextBytes           int64
	MaxThinkingBytes       int64

	MaxRequestMessages        int
	MaxProjectedMessages      int
	MaxToolDefinitions        int
	MaxRecalledMemoryMessages int
	MaxCompressionSummaries   int
	MaxToolResultBlocks       int
	MaxArtifactBlocks         int
}

// ValidateInstructionStackBudget checks every model.call.started Context
// Manifest in a trace without reading model Request bodies.
func ValidateInstructionStackBudget(events []event.Event, budget InstructionStackBudget) error {
	if err := budget.validate(); err != nil {
		return err
	}
	modelCalls := 0
	for _, value := range events {
		if value.Type != event.ModelCallStarted {
			continue
		}
		modelCalls++
		payload, err := DecodePayload[event.ModelCallPayload](value)
		if err != nil {
			return fmt.Errorf("decode model.call.started %q: %w", value.ID, err)
		}
		if payload.Context == nil {
			return fmt.Errorf("model.call.started %q has no Context Manifest", value.ID)
		}
		if err := payload.Context.Validate(); err != nil {
			return fmt.Errorf("model.call.started %q has invalid Context Manifest: %w", value.ID, err)
		}
		if err := budget.check(payload.CallID, *payload.Context); err != nil {
			return err
		}
	}
	if modelCalls == 0 {
		return fmt.Errorf("instruction stack budget requires at least one model.call.started event")
	}
	return nil
}

// ValidateContextBudget checks every model.call.started Context Manifest in a
// trace against configured content-free request-shape budgets.
func ValidateContextBudget(events []event.Event, budget ContextBudget) error {
	if err := budget.validate(); err != nil {
		return err
	}
	modelCalls := 0
	for _, value := range events {
		if value.Type != event.ModelCallStarted {
			continue
		}
		modelCalls++
		payload, err := DecodePayload[event.ModelCallPayload](value)
		if err != nil {
			return fmt.Errorf("decode model.call.started %q: %w", value.ID, err)
		}
		if payload.Context == nil {
			return fmt.Errorf("model.call.started %q has no Context Manifest", value.ID)
		}
		if err := payload.Context.Validate(); err != nil {
			return fmt.Errorf("model.call.started %q has invalid Context Manifest: %w", value.ID, err)
		}
		if err := budget.check(payload.CallID, *payload.Context); err != nil {
			return err
		}
	}
	if modelCalls == 0 {
		return fmt.Errorf("context budget requires at least one model.call.started event")
	}
	return nil
}

func (b ContextBudget) validate() error {
	if err := b.InstructionStack.validate(); err != nil {
		return err
	}
	for _, check := range []struct {
		name  string
		value int64
	}{
		{name: "MaxToolDefinitionBytes", value: b.MaxToolDefinitionBytes},
		{name: "MaxToolArgumentBytes", value: b.MaxToolArgumentBytes},
		{name: "MaxTextBytes", value: b.MaxTextBytes},
		{name: "MaxThinkingBytes", value: b.MaxThinkingBytes},
	} {
		if check.value < 0 {
			return fmt.Errorf("context budget %s cannot be negative", check.name)
		}
	}
	for _, check := range []struct {
		name  string
		value int
	}{
		{name: "MaxRequestMessages", value: b.MaxRequestMessages},
		{name: "MaxProjectedMessages", value: b.MaxProjectedMessages},
		{name: "MaxToolDefinitions", value: b.MaxToolDefinitions},
		{name: "MaxRecalledMemoryMessages", value: b.MaxRecalledMemoryMessages},
		{name: "MaxCompressionSummaries", value: b.MaxCompressionSummaries},
		{name: "MaxToolResultBlocks", value: b.MaxToolResultBlocks},
		{name: "MaxArtifactBlocks", value: b.MaxArtifactBlocks},
	} {
		if check.value < 0 {
			return fmt.Errorf("context budget %s cannot be negative", check.name)
		}
	}
	return nil
}

func (b ContextBudget) check(callID string, snapshot event.ModelContextPayload) error {
	if err := b.InstructionStack.check(callID, snapshot); err != nil {
		return err
	}
	for _, check := range []struct {
		name   string
		actual int64
		limit  int64
	}{
		{name: "ToolDefinitionBytes", actual: snapshot.ToolDefinitionBytes, limit: b.MaxToolDefinitionBytes},
		{name: "ToolArgumentBytes", actual: snapshot.ToolArgumentBytes, limit: b.MaxToolArgumentBytes},
		{name: "TextBytes", actual: snapshot.TextBytes, limit: b.MaxTextBytes},
		{name: "ThinkingBytes", actual: snapshot.ThinkingBytes, limit: b.MaxThinkingBytes},
	} {
		if check.limit > 0 && check.actual > check.limit {
			return fmt.Errorf("model call %q context %s = %d exceeds budget %d", callID, check.name, check.actual, check.limit)
		}
	}
	for _, check := range []struct {
		name   string
		actual int
		limit  int
	}{
		{name: "RequestMessageCount", actual: snapshot.RequestMessageCount, limit: b.MaxRequestMessages},
		{name: "ProjectedMessageCount", actual: snapshot.ProjectedMessageCount, limit: b.MaxProjectedMessages},
		{name: "ToolDefinitionCount", actual: snapshot.ToolDefinitionCount, limit: b.MaxToolDefinitions},
		{name: "RecalledMemoryMessages", actual: snapshot.RecalledMemoryMessages, limit: b.MaxRecalledMemoryMessages},
		{name: "CompressionSummaries", actual: snapshot.CompressionSummaries, limit: b.MaxCompressionSummaries},
		{name: "ToolResultBlocks", actual: snapshot.ToolResultBlocks, limit: b.MaxToolResultBlocks},
		{name: "ArtifactBlocks", actual: snapshot.ArtifactBlocks, limit: b.MaxArtifactBlocks},
	} {
		if check.limit > 0 && check.actual > check.limit {
			return fmt.Errorf("model call %q context %s = %d exceeds budget %d", callID, check.name, check.actual, check.limit)
		}
	}
	return nil
}

func (b InstructionStackBudget) validate() error {
	for name, value := range map[string]int64{
		"MaxBytes":             b.MaxBytes,
		"MaxSystemPromptBytes": b.MaxSystemPromptBytes,
		"MaxProjectBytes":      b.MaxProjectBytes,
		"MaxTaskRuleBytes":     b.MaxTaskRuleBytes,
		"MaxPolicyBytes":       b.MaxPolicyBytes,
		"MaxKnowledgeBytes":    b.MaxKnowledgeBytes,
		"MaxSkillBytes":        b.MaxSkillBytes,
	} {
		if value < 0 {
			return fmt.Errorf("instruction stack budget %s cannot be negative", name)
		}
	}
	return nil
}

func (b InstructionStackBudget) check(callID string, snapshot event.ModelContextPayload) error {
	for _, check := range []struct {
		name   string
		actual int64
		limit  int64
	}{
		{name: "total", actual: snapshot.InstructionStackBytes, limit: b.MaxBytes},
		{name: "system_prompt", actual: snapshot.InstructionStackSystemPromptBytes, limit: b.MaxSystemPromptBytes},
		{name: "project", actual: snapshot.InstructionStackProjectBytes, limit: b.MaxProjectBytes},
		{name: "task_rule", actual: snapshot.InstructionStackTaskRuleBytes, limit: b.MaxTaskRuleBytes},
		{name: "policy", actual: snapshot.InstructionStackPolicyBytes, limit: b.MaxPolicyBytes},
		{name: "knowledge", actual: snapshot.InstructionStackKnowledgeBytes, limit: b.MaxKnowledgeBytes},
		{name: "skill", actual: snapshot.InstructionStackSkillBytes, limit: b.MaxSkillBytes},
	} {
		if check.limit > 0 && check.actual > check.limit {
			return fmt.Errorf("model call %q instruction stack %s bytes = %d exceeds budget %d", callID, check.name, check.actual, check.limit)
		}
	}
	return nil
}
