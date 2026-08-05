package record

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestSafeSanitizerRedactsDeltasAndUnknownPayloads(t *testing.T) {
	t.Parallel()

	journal, _ := NewMemoryJournal(MemoryOptions{})
	recorder, _ := New(journal, Options{})
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	values := []event.Event{
		recordEvent("run_safe", "start", event.RunStarted, 1, base, "", `{"agent":"test"}`),
		recordEvent("run_safe", "model", event.ModelCallStarted, 2, base.Add(time.Millisecond), "", `{"call_id":"call","model":{"provider":"test","model":"stable"},"attempt":1,"context":{"projected_message_count":2,"request_message_count":2,"system_message_count":1,"user_message_count":1,"public_message_count":1,"internal_message_count":1,"recalled_memory_messages":0,"compression_summaries":0,"text_blocks":2,"text_bytes":12,"thinking_blocks":0,"thinking_bytes":0,"image_blocks":0,"tool_call_blocks":0,"tool_argument_bytes":0,"tool_result_blocks":0,"artifact_blocks":0,"tool_definition_count":0,"tool_definition_bytes":0}}`),
		recordEvent("run_safe", "delta", event.ModelCallDelta, 3, base.Add(2*time.Millisecond), "model", `{"call_id":"call","delta":{"type":"tool_call","index":0,"text":"secret text","tool_call":{"index":0,"id":"secret-id","name":"secret-tool","arguments_fragment":"secret-arguments"},"provider_error":{"class":"provider","message":"secret-error"}}}`),
		recordEvent("run_safe", "future", event.Type("future.event"), 4, base.Add(3*time.Millisecond), "", `{"secret":"future-secret"}`),
		recordEvent("run_safe", "model_done", event.ModelCallCompleted, 5, base.Add(4*time.Millisecond), "model", `{"call_id":"call","model":{"provider":"test","model":"stable"},"attempt":1,"status":"completed"}`),
		recordEvent("run_safe", "terminal", event.RunCompleted, 6, base.Add(5*time.Millisecond), "", `{"status":"completed","usage":{}}`),
	}
	for _, value := range values {
		if err := recorder.Sink(value); err != nil {
			t.Fatal(err)
		}
	}
	recording, _ := journal.Recording("run_safe")
	modelStart := recording.Entries[1]
	if modelStart.PayloadRedacted || !strings.Contains(string(modelStart.Event.Payload), `"projected_message_count":2`) {
		t.Fatalf("safe Model Context manifest was not retained: %#v", modelStart)
	}
	delta := recording.Entries[2]
	if !delta.PayloadRedacted {
		t.Fatal("Model Delta was not marked redacted")
	}
	encoded := string(delta.Event.Payload)
	for _, secret := range []string{"secret text", "secret-id", "secret-tool", "secret-arguments", "secret-error"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("sanitized Delta contains %q: %s", secret, encoded)
		}
	}
	unknown := recording.Entries[3]
	if !unknown.PayloadRedacted || len(unknown.Event.Payload) != 0 {
		t.Fatalf("unknown payload was retained: %#v", unknown)
	}
}

func TestFullSanitizerRequiresExplicitSelection(t *testing.T) {
	t.Parallel()

	value := recordEvent("run_full", "delta", event.ModelCallDelta, 1, time.Now(), "", `{"secret":"retained"}`)
	result, err := (FullSanitizer{}).Sanitize(value)
	if err != nil || result.PayloadRedacted || !strings.Contains(string(result.Event.Payload), "retained") {
		t.Fatalf("FullSanitizer() = %#v, %v", result, err)
	}
}

func TestRecorderRejectsBrokenSequenceParentAndSanitizerEnvelope(t *testing.T) {
	t.Parallel()

	base := time.Now()
	for _, test := range []struct {
		name      string
		options   Options
		second    event.Event
		wantError string
	}{
		{
			name: "sequence", second: recordEvent("run_sequence", "gap", event.RuntimeError, 3, base.Add(time.Second), "", `{}`),
			wantError: "sequence",
		},
		{
			name: "parent", second: recordEvent("run_parent", "child", event.RuntimeError, 2, base.Add(time.Second), "missing", `{}`),
			wantError: "parent",
		},
		{
			name: "envelope",
			options: Options{Sanitizer: SanitizerFunc(func(value event.Event) (SanitizedEvent, error) {
				if value.Type != event.RunStarted {
					value.ID = "changed"
				}
				return SanitizedEvent{Event: value}, nil
			})},
			second:    recordEvent("run_envelope", "second", event.RuntimeError, 2, base.Add(time.Second), "", `{}`),
			wantError: "only Event payload",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, _ := NewMemoryJournal(MemoryOptions{})
			recorder, err := New(journal, test.options)
			if err != nil {
				t.Fatal(err)
			}
			start := recordEvent(test.second.RunID, "start", event.RunStarted, 1, base, "", `{"agent":"test"}`)
			if err := recorder.Sink(start); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Sink(test.second); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Sink() error = %v", err)
			}
		})
	}
}

func TestSanitizerMustDeclarePayloadChanges(t *testing.T) {
	t.Parallel()

	memory, _ := NewMemoryJournal(MemoryOptions{})
	recorder, _ := New(memory, Options{Sanitizer: SanitizerFunc(func(value event.Event) (SanitizedEvent, error) {
		value.Payload = []byte(`{}`)
		return SanitizedEvent{Event: value}, nil
	})})
	start := recordEvent("run_redaction_flag", "start", event.RunStarted, 1, time.Now(), "", `{"agent":"test"}`)
	if err := recorder.Sink(start); err == nil || !strings.Contains(err.Error(), "marking it redacted") {
		t.Fatalf("Sink() error = %v", err)
	}
}

func TestRecorderContainsExtensionPanicsAndEnforcesBounds(t *testing.T) {
	t.Parallel()

	start := recordEvent("run_panic", "start", event.RunStarted, 1, time.Now(), "", `{"agent":"test"}`)
	panicJournal, _ := New(JournalFunc(func(Entry) error { panic("journal boom") }), Options{})
	if err := panicJournal.Sink(start); err == nil || !strings.Contains(err.Error(), "Journal panic") {
		t.Fatalf("Journal panic error = %v", err)
	}

	memory, _ := NewMemoryJournal(MemoryOptions{})
	panicSanitizer, _ := New(memory, Options{Sanitizer: SanitizerFunc(func(event.Event) (SanitizedEvent, error) {
		panic("sanitizer boom")
	})})
	if err := panicSanitizer.Sink(start); err == nil || !strings.Contains(err.Error(), "Sanitizer panic") {
		t.Fatalf("Sanitizer panic error = %v", err)
	}

	failedJournal, _ := New(JournalFunc(func(Entry) error { return errors.New("disk unavailable") }), Options{})
	if err := failedJournal.Sink(start); err == nil || !strings.Contains(err.Error(), "disk unavailable") {
		t.Fatalf("Journal error = %v", err)
	}

	bounded, _ := New(memory, Options{MaxActiveRuns: 1})
	if err := bounded.Sink(start); err != nil {
		t.Fatal(err)
	}
	other := recordEvent("run_other", "other_start", event.RunStarted, 1, time.Now(), "", `{"agent":"test"}`)
	if err := bounded.Sink(other); err == nil || !strings.Contains(err.Error(), "active Run limit") {
		t.Fatalf("active Run bound error = %v", err)
	}

	eventBoundMemory, _ := NewMemoryJournal(MemoryOptions{})
	eventBound, _ := New(eventBoundMemory, Options{MaxEventsPerRun: 1})
	if err := eventBound.Sink(start); err != nil {
		t.Fatal(err)
	}
	terminal := recordEvent("run_panic", "done", event.RunCompleted, 2, time.Now().Add(time.Millisecond), "", `{"status":"completed","usage":{}}`)
	if err := eventBound.Sink(terminal); err == nil || !strings.Contains(err.Error(), "per-Run Event limit") {
		t.Fatalf("per-Run Event bound error = %v", err)
	}
}
