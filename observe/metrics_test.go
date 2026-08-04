package observe

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type measurementRecorder struct {
	mu     sync.Mutex
	values []Measurement
}

func (r *measurementRecorder) Record(value Measurement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, value.Clone())
	return nil
}

func (r *measurementRecorder) snapshot() []Measurement {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]Measurement, len(r.values))
	for index, value := range r.values {
		values[index] = value.Clone()
	}
	return values
}

func TestMetricsDerivesLowCardinalityCountsDurationsAndUsage(t *testing.T) {
	t.Parallel()

	recorder := &measurementRecorder{}
	metrics, err := NewMetrics(recorder)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	identity := model.Identity{Provider: "test", Model: "stable"}
	events := []event.Event{
		testEvent(t, "run_metrics", "evt_run", event.RunStarted, 1, startedAt, "", event.RunStartedPayload{Agent: "planner"}),
		testEvent(t, "run_metrics", "evt_model", event.ModelCallStarted, 2, startedAt.Add(time.Millisecond), "", event.ModelCallPayload{CallID: "call_1", Model: identity, Attempt: 1}),
		testEvent(t, "run_metrics", "evt_model_done", event.ModelCallCompleted, 3, startedAt.Add(11*time.Millisecond), "evt_model", event.ModelCallPayload{CallID: "call_1", Model: identity, Attempt: 1, Status: "completed"}),
		testEvent(t, "run_metrics", "evt_terminal", event.RunCompleted, 4, startedAt.Add(21*time.Millisecond), "", event.RunTerminalPayload{
			Status: "completed", Iterations: 1,
			Usage: model.Usage{InputTokens: 12, OutputTokens: 3, Source: model.UsageSourceProvider},
		}),
	}
	for _, value := range events {
		if err := metrics.Sink(value); err != nil {
			t.Fatal(err)
		}
	}
	values := recorder.snapshot()
	assertMeasurement(t, values, MetricModelCallCount, 1)
	assertMeasurement(t, values, MetricModelCallDuration, 10)
	assertMeasurement(t, values, MetricRunCount, 1)
	assertMeasurement(t, values, MetricRunDuration, 21)
	assertMeasurement(t, values, MetricTokenUsage, 12)
	assertMeasurement(t, values, MetricTokenUsage, 3)
	for _, value := range values {
		for _, attribute := range value.Attributes {
			if attribute.Key == "run.id" || attribute.Key == "event.id" || attribute.Key == "error.code" || attribute.Value == "run_metrics" {
				t.Fatalf("high-cardinality metric attribute: %#v", attribute)
			}
		}
	}
	if len(metrics.runs) != 0 || len(metrics.operations) != 0 {
		t.Fatalf("metrics retained completed Run state: runs=%d operations=%d", len(metrics.runs), len(metrics.operations))
	}
}

func TestMetricsSupportsConcurrentRunsAndCleansIncompleteOperations(t *testing.T) {
	t.Parallel()

	recorder := &measurementRecorder{}
	metrics, err := NewMetrics(recorder)
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			runID := fmt.Sprintf("run_%02d", index)
			base := time.Date(2030, 1, 1, 0, 0, index, 0, time.UTC)
			values := []event.Event{
				testEvent(t, runID, "start_"+runID, event.RunStarted, 1, base, "", event.RunStartedPayload{Agent: "shared"}),
				testEvent(t, runID, "tool_"+runID, event.ToolCallStarted, 2, base.Add(time.Millisecond), "", event.ToolCallPayload{CallID: "call_" + runID, Name: "toolkit.read"}),
				testEvent(t, runID, "terminal_"+runID, event.RunCanceled, 3, base.Add(2*time.Millisecond), "", event.RunTerminalPayload{Status: "canceled"}),
			}
			for _, value := range values {
				if sinkErr := metrics.Sink(value); sinkErr != nil {
					t.Errorf("Sink(%s): %v", runID, sinkErr)
					return
				}
			}
		}(index)
	}
	group.Wait()
	if len(metrics.runs) != 0 || len(metrics.operations) != 0 {
		t.Fatalf("metrics state leaked: runs=%d operations=%d", len(metrics.runs), len(metrics.operations))
	}
}

func TestMetricsRejectsUnpairedOutcome(t *testing.T) {
	t.Parallel()

	metrics, err := NewMetrics(RecorderFunc(func(Measurement) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	value := testEvent(t, "run_missing", "done", event.ToolCallCompleted, 1, time.Now(), "missing", event.ToolCallPayload{
		CallID: "call", Name: "tool", Status: "completed",
	})
	if err := metrics.Sink(value); err == nil {
		t.Fatal("Metrics accepted an unpaired outcome")
	}
}

func assertMeasurement(t *testing.T, values []Measurement, name string, target float64) {
	t.Helper()
	for _, value := range values {
		if value.Name == name && value.Value == target {
			return
		}
	}
	t.Fatalf("measurement %s=%v not found in %#v", name, target, values)
}

func testEvent(t *testing.T, runID, id string, eventType event.Type, sequence int64, createdAt time.Time, parentID string, payload any) event.Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	value := event.Event{
		ProtocolVersion: event.ProtocolV1, ID: id, RunID: runID, ParentID: parentID,
		Type: eventType, Sequence: sequence, CreatedAt: createdAt, Payload: raw,
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	return value
}
