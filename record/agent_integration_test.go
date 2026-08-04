package record_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/record"
)

func TestAgentRunProducesCompletePrivacySafeRecording(t *testing.T) {
	t.Parallel()

	journal, err := record.NewMemoryJournal(record.MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := record.New(journal, record.Options{})
	if err != nil {
		t.Fatal(err)
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Deltas: []model.Delta{{Type: model.DeltaText, Index: 0, Text: "private streamed answer"}},
		Response: model.Response{
			Message:    message.Message{Content: []message.Content{{Type: message.ContentText, Text: "final answer"}}},
			StopReason: model.StopReasonComplete,
			Usage:      model.Usage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6, Source: model.UsageSourceProvider},
		},
	})
	runtime, err := agent.New(agent.Config{
		Name: "recorder-test", Model: script,
		ModelIdentity: model.Identity{Provider: "test", Model: "stable"},
		IDs:           agenttest.SequentialIDs(),
		Now:           agenttest.StepClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := runtime.RunStream(context.Background(), message.Message{
		Role: message.RoleUser, Visibility: message.VisibilityPublic,
		Content: []message.Content{{Type: message.ContentText, Text: "question"}},
	}, recorder.Sink)
	if runErr != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("RunStream() = %#v, %v", result, runErr)
	}
	recording, exists := journal.Recording("run_1")
	if !exists {
		t.Fatal("Agent Recording not found")
	}
	if err := recording.ValidateComplete(); err != nil {
		t.Fatal(err)
	}
	if len(recording.Entries) != len(result.Events) {
		t.Fatalf("recorded Events = %d, Result Events = %d", len(recording.Entries), len(result.Events))
	}
	for _, entry := range recording.Entries {
		if strings.Contains(string(entry.Event.Payload), "private streamed answer") {
			t.Fatal("default Recording retained streamed model content")
		}
	}
}
