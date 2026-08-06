package observe

import (
	"context"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestAsyncMetricsRecordsGaugesAndCounterDeltas(t *testing.T) {
	control := newBlockingSink()
	async, err := NewAsyncSink(control.sink, AsyncOptions{QueueSize: 1, DropPolicy: AsyncDropNewest})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &measurementRecorder{}
	metrics, err := NewAsyncMetrics(async, recorder, AsyncMetricsOptions{Name: "telemetry"})
	if err != nil {
		t.Fatal(err)
	}
	if err := metrics.Record(); err != nil {
		t.Fatalf("Record(initial) error = %v", err)
	}

	events := asyncEvents(t, "async_metrics", 3)
	if err := async.Sink(events[0]); err != nil {
		t.Fatalf("Sink(first) error = %v", err)
	}
	control.waitStarted(t, "evt_1")
	if err := async.Sink(events[1]); err != nil {
		t.Fatalf("Sink(second) error = %v", err)
	}
	if err := async.Sink(events[2]); err != nil {
		t.Fatalf("Sink(third) error = %v", err)
	}
	control.release()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := metrics.Record(); err != nil {
		t.Fatalf("Record(final) error = %v", err)
	}
	if err := metrics.Record(); err != nil {
		t.Fatalf("Record(repeated) error = %v", err)
	}

	values := recorder.snapshot()
	assertAsyncMeasurement(t, values, MetricAsyncSubmitted, MetricCounter, 3, "observer.name", "telemetry")
	assertAsyncMeasurement(t, values, MetricAsyncProcessed, MetricCounter, 2, "observer.name", "telemetry")
	assertAsyncMeasurement(t, values, MetricAsyncDelegateCalls, MetricCounter, 2, "observer.name", "telemetry")
	assertAsyncMeasurement(t, values, MetricAsyncDropped, MetricCounter, 1, "reason", "newest")
	assertAsyncMeasurement(t, values, MetricAsyncClosed, MetricGauge, 1, "observer.name", "telemetry")
	if countMeasurements(values, MetricAsyncSubmitted) != 1 ||
		countMeasurements(values, MetricAsyncProcessed) != 1 ||
		countMeasurements(values, MetricAsyncDropped) != 1 {
		t.Fatalf("counter deltas were emitted more than once: %#v", values)
	}
}

func TestAsyncMetricsValidatesObserverName(t *testing.T) {
	async, err := NewAsyncSink(func(event.Event) error { return nil }, AsyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.Shutdown(context.Background()) })
	recorder := RecorderFunc(func(Measurement) error { return nil })
	for _, name := range []string{"", "contains space", strings.Repeat("a", 65)} {
		if _, err := NewAsyncMetrics(async, recorder, AsyncMetricsOptions{Name: name}); err == nil {
			t.Fatalf("NewAsyncMetrics() accepted name %q", name)
		}
	}
	if _, err := NewAsyncMetrics(async, recorder, AsyncMetricsOptions{Name: "otel.primary"}); err != nil {
		t.Fatalf("NewAsyncMetrics() error = %v", err)
	}
}

func assertAsyncMeasurement(t *testing.T, values []Measurement, name string, kind MetricKind, target float64, attrKey, attrValue string) {
	t.Helper()
	for _, value := range values {
		if value.Name != name || value.Kind != kind || value.Value != target {
			continue
		}
		for _, attribute := range value.Attributes {
			if attribute.Key == attrKey && attribute.Value == attrValue {
				return
			}
		}
	}
	t.Fatalf("measurement %s %s=%v %s=%s not found in %#v", name, kind, target, attrKey, attrValue, values)
}

func countMeasurements(values []Measurement, name string) int {
	count := 0
	for _, value := range values {
		if value.Name == name {
			count++
		}
	}
	return count
}
