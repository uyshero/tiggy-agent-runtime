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
