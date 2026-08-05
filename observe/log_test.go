package observe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestLogSinkExcludesModelDeltaBodies(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink, err := LogSink(logger, LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := testEvent(t, "run_log", "evt_log", event.ModelCallDelta, 1, time.Now(), "", event.ModelDeltaPayload{
		CallID: "call_1",
		Delta: model.Delta{
			Type: model.DeltaToolCall,
			Text: "sensitive model text",
			ToolCall: &model.ToolCallDelta{
				ID: "tool_1", Name: "secret_tool", ArgumentsFragment: `{"password":"secret-value"}`,
			},
		},
	})
	if err := sink(value); err != nil {
		t.Fatal(err)
	}
	logged := output.String()
	for _, secret := range []string{"sensitive model text", "secret_tool", "secret-value", "password"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("default log contains %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, `"model.delta_type":"tool_call"`) || !strings.Contains(logged, `"run.id":"run_log"`) {
		t.Fatalf("missing diagnostic fields: %s", logged)
	}
}

func TestLogSinkRejectsNilLogger(t *testing.T) {
	t.Parallel()

	if _, err := LogSink(nil, LogOptions{}); err == nil {
		t.Fatal("LogSink accepted a nil Logger")
	}
}

func TestLogSinkEmitsContentFreeModelContextSnapshot(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sink, err := LogSink(logger, LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := testEvent(t, "run_context", "evt_context", event.ModelCallStarted, 1, time.Now(), "", event.ModelCallPayload{
		CallID: "call_context", Model: model.Identity{Provider: "test", Model: "model"}, Attempt: 1,
		Context: &event.ModelContextPayload{
			ProjectedMessageCount: 3, RequestMessageCount: 2,
			SystemMessageCount: 1, UserMessageCount: 1,
			PublicMessageCount: 1, InternalMessageCount: 1,
			InstructionStackMessages: 1, InstructionStackBytes: 177, CompressionSummaries: 1, TextBlocks: 2, TextBytes: 123,
			InstructionStackSystemPromptBytes: 11, InstructionStackProjectBytes: 12, InstructionStackTaskRuleBytes: 13,
			InstructionStackPolicyBytes: 14, InstructionStackKnowledgeBytes: 15, InstructionStackSkillBytes: 16,
			ToolDefinitionCount: 1, ToolDefinitionBytes: 456,
		},
	})
	if err := sink(value); err != nil {
		t.Fatal(err)
	}
	logged := output.String()
	for _, expected := range []string{
		`"context.projected_message_count":3`, `"context.request_message_count":2`,
		`"context.instruction_stack_messages":1`, `"context.instruction_stack_bytes":177`, `"context.compression_summaries":1`,
		`"context.instruction_stack_system_prompt_bytes":11`, `"context.instruction_stack_project_bytes":12`,
		`"context.instruction_stack_task_rule_bytes":13`, `"context.instruction_stack_policy_bytes":14`,
		`"context.instruction_stack_knowledge_bytes":15`, `"context.instruction_stack_skill_bytes":16`,
		`"context.text_bytes":123`, `"context.tool_definition_bytes":456`,
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("log missing %s: %s", expected, logged)
		}
	}
}

func TestObserversIgnoreUnknownEventPayloads(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logSink, err := LogSink(logger, LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := NewMetrics(RecorderFunc(func(Measurement) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	value := testEvent(t, "run_future", "evt_future", event.Type("future.operation"), 1, time.Now(), "", map[string]string{
		"secret": "must-not-be-decoded",
	})
	if err := logSink(value); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Sink(value); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "must-not-be-decoded") || !strings.Contains(output.String(), "future.operation") {
		t.Fatalf("unexpected unknown Event log: %s", output.String())
	}
}
