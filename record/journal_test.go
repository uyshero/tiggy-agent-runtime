package record

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestJSONLRoundTripInterleavedAndIncompleteRecordings(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	journal, err := NewJSONLJournal(&output, JSONLOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := New(journal, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	values := []event.Event{
		recordEvent("run_a", "a_start", event.RunStarted, 1, base, "", `{"agent":"a"}`),
		recordEvent("run_b", "b_start", event.RunStarted, 1, base, "", `{"agent":"b"}`),
		recordEvent("run_a", "a_done", event.RunCompleted, 2, base.Add(time.Millisecond), "", `{"status":"completed","usage":{}}`),
		recordEvent("run_b", "b_error", event.RuntimeError, 2, base.Add(time.Millisecond), "", `{"class":"test","message":"safe"}`),
	}
	for _, value := range values {
		if err := recorder.Sink(value); err != nil {
			t.Fatal(err)
		}
	}
	recordings, err := ReadJSONL(bytes.NewReader(output.Bytes()), ReadOptions{})
	if err != nil || len(recordings) != 2 || !recordings[0].Complete() || recordings[1].Complete() {
		t.Fatalf("ReadJSONL() = %#v, %v", recordings, err)
	}
	if _, err := ReadJSONL(bytes.NewReader(output.Bytes()), ReadOptions{RequireComplete: true}); err == nil {
		t.Fatal("RequireComplete accepted a partial Recording")
	}

	terminal := recordEvent("run_b", "b_done", event.RunFailed, 3, base.Add(2*time.Millisecond), "", `{"status":"failed","usage":{}}`)
	if err := recorder.Sink(terminal); err != nil {
		t.Fatal(err)
	}
	recordings, err = ReadJSONL(bytes.NewReader(output.Bytes()), ReadOptions{RequireComplete: true})
	if err != nil || len(recordings) != 2 {
		t.Fatalf("complete ReadJSONL() = %#v, %v", recordings, err)
	}
}

func TestJournalsEnforceCapacityAndReadBounds(t *testing.T) {
	t.Parallel()

	memory, _ := NewMemoryJournal(MemoryOptions{MaxEntries: 1, MaxBytes: DefaultMemoryMaxBytes})
	recorder, _ := New(memory, Options{})
	start := recordEvent("run_capacity", "start", event.RunStarted, 1, time.Now(), "", `{"agent":"test"}`)
	terminal := recordEvent("run_capacity", "done", event.RunCompleted, 2, time.Now().Add(time.Millisecond), "", `{"status":"completed","usage":{}}`)
	if err := recorder.Sink(start); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Sink(terminal); err == nil {
		t.Fatal("Memory Journal accepted Entry beyond capacity")
	}
	partial, _ := memory.Recording("run_capacity")
	if partial.Complete() || partial.Validate() != nil {
		t.Fatalf("capacity failure corrupted partial Recording: %#v", partial)
	}

	var output bytes.Buffer
	jsonl, _ := NewJSONLJournal(&output, JSONLOptions{})
	jsonRecorder, _ := New(jsonl, Options{})
	if err := jsonRecorder.Sink(start); err != nil {
		t.Fatal(err)
	}
	if err := jsonRecorder.Sink(terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJSONL(bytes.NewReader(output.Bytes()), ReadOptions{MaxEntries: 1}); err == nil {
		t.Fatal("ReadJSONL accepted Entries beyond configured bound")
	}
}

func TestRecorderAndMemoryJournalSupportConcurrentRuns(t *testing.T) {
	t.Parallel()

	journal, _ := NewMemoryJournal(MemoryOptions{})
	recorder, _ := New(journal, Options{})
	const count = 64
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			runID := fmt.Sprintf("run_%03d", index)
			base := time.Date(2030, 1, 1, 0, 0, index, 0, time.UTC)
			start := recordEvent(runID, "start_"+runID, event.RunStarted, 1, base, "", `{"agent":"shared"}`)
			terminal := recordEvent(runID, "done_"+runID, event.RunCompleted, 2, base.Add(time.Millisecond), "", `{"status":"completed","usage":{}}`)
			if err := recorder.Sink(start); err != nil {
				t.Errorf("start %s: %v", runID, err)
				return
			}
			if err := recorder.Sink(terminal); err != nil {
				t.Errorf("terminal %s: %v", runID, err)
			}
		}(index)
	}
	group.Wait()
	recordings := journal.Recordings()
	if len(recordings) != count {
		t.Fatalf("Recording count = %d", len(recordings))
	}
	for _, recording := range recordings {
		if err := recording.ValidateComplete(); err != nil {
			t.Fatalf("Recording %s: %v", recording.RunID, err)
		}
	}
}
