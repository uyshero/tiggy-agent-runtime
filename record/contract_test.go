package record

import (
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestRecordingValidatesCompletenessOwnershipAndTampering(t *testing.T) {
	t.Parallel()

	journal, err := NewMemoryJournal(MemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := New(journal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	values := []event.Event{
		recordEvent("run_integrity", "start", event.RunStarted, 1, base, "", `{"agent":"test"}`),
		recordEvent("run_integrity", "operation", event.ToolCallStarted, 2, base.Add(time.Millisecond), "", `{"call_id":"call","name":"tool"}`),
		recordEvent("run_integrity", "operation_done", event.ToolCallCompleted, 3, base.Add(2*time.Millisecond), "operation", `{"call_id":"call","name":"tool","status":"completed"}`),
		recordEvent("run_integrity", "terminal", event.RunCompleted, 4, base.Add(3*time.Millisecond), "", `{"status":"completed","iterations":1,"usage":{}}`),
	}
	for _, value := range values {
		if err := recorder.Sink(value); err != nil {
			t.Fatal(err)
		}
	}
	recording, exists := journal.Recording("run_integrity")
	if !exists {
		t.Fatal("completed Recording not found")
	}
	if err := recording.ValidateComplete(); err != nil {
		t.Fatal(err)
	}
	if recording.FinalDigest() == "" || recording.Entries[1].PreviousDigest != recording.Entries[0].Digest {
		t.Fatalf("invalid digest chain: %#v", recording)
	}

	recording.Entries[0].Event.Payload[0] = 'x'
	again, _ := journal.Recording("run_integrity")
	if again.Entries[0].Event.Payload[0] == 'x' {
		t.Fatal("Recording snapshot mutated Memory Journal storage")
	}

	tampered := again.Clone()
	tampered.Entries[1].Event.Payload = []byte(`{"changed":true}`)
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampered Recording error = %v", err)
	}
}

func TestIncompleteRecordingIsValidButNotComplete(t *testing.T) {
	t.Parallel()

	start := recordEvent("run_partial", "start", event.RunStarted, 1, time.Now(), "", `{"agent":"test"}`)
	entry, err := newEntry(start, "", false)
	if err != nil {
		t.Fatal(err)
	}
	recording := Recording{ProtocolVersion: ProtocolV1, RunID: start.RunID, Entries: []Entry{entry}}
	if err := recording.Validate(); err != nil {
		t.Fatal(err)
	}
	if recording.Complete() || recording.ValidateComplete() == nil {
		t.Fatal("partial Recording reported complete")
	}
}

func recordEvent(runID, id string, eventType event.Type, sequence int64, createdAt time.Time, parentID, payload string) event.Event {
	return event.Event{
		ProtocolVersion: event.ProtocolV1, ID: id, RunID: runID, ParentID: parentID,
		Type: eventType, Sequence: sequence, CreatedAt: createdAt, Payload: []byte(payload),
	}
}
