package otel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/observe"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestObserverExportsCorrelatedSpansAndMetrics(t *testing.T) {
	t.Parallel()

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	metricReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	observer, err := New(Options{TracerProvider: tracerProvider, MeterProvider: meterProvider})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	identity := model.Identity{Provider: "test", Model: "stable"}
	values := []event.Event{
		otelTestEvent(t, "run_otel", "run_start", event.RunStarted, 1, startedAt, "", event.RunStartedPayload{Agent: "planner"}),
		otelTestEvent(t, "run_otel", "model_start", event.ModelCallStarted, 2, startedAt.Add(time.Millisecond), "", event.ModelCallPayload{CallID: "call_1", Model: identity, Attempt: 1}),
		otelTestEvent(t, "run_otel", "model_done", event.ModelCallCompleted, 3, startedAt.Add(11*time.Millisecond), "model_start", event.ModelCallPayload{CallID: "call_1", Model: identity, Attempt: 1, Status: "completed"}),
		otelTestEvent(t, "run_otel", "run_done", event.RunCompleted, 4, startedAt.Add(20*time.Millisecond), "", event.RunTerminalPayload{
			Status: "completed", Iterations: 1,
			Usage: model.Usage{InputTokens: 10, OutputTokens: 2, Source: model.UsageSourceProvider},
		}),
	}
	for _, value := range values {
		if err := observer.Sink(value); err != nil {
			t.Fatal(err)
		}
	}

	ended := spanRecorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended span count = %d", len(ended))
	}
	var runSpan, modelSpan sdktrace.ReadOnlySpan
	for _, span := range ended {
		switch span.Name() {
		case "tiggy.agent.run":
			runSpan = span
		case "tiggy.agent.model.call":
			modelSpan = span
		}
	}
	if runSpan == nil || modelSpan == nil {
		t.Fatalf("spans = %#v", ended)
	}
	if modelSpan.Parent().SpanID() != runSpan.SpanContext().SpanID() {
		t.Fatal("model span is not a child of the Run span")
	}
	if modelSpan.EndTime().Sub(modelSpan.StartTime()) != 10*time.Millisecond || runSpan.EndTime().Sub(runSpan.StartTime()) != 20*time.Millisecond {
		t.Fatalf("unexpected span durations: model=%s run=%s",
			modelSpan.EndTime().Sub(modelSpan.StartTime()), runSpan.EndTime().Sub(runSpan.StartTime()))
	}
	if len(observer.trace.runs) != 0 || len(observer.trace.spans) != 0 {
		t.Fatalf("trace state leaked: runs=%d spans=%d", len(observer.trace.runs), len(observer.trace.spans))
	}

	var exported metricdata.ResourceMetrics
	if err := metricReader.Collect(context.Background(), &exported); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]struct{})
	for _, scope := range exported.ScopeMetrics {
		for _, metricValue := range scope.Metrics {
			names[metricValue.Name] = struct{}{}
		}
	}
	for _, name := range []string{
		observe.MetricRunCount, observe.MetricRunDuration,
		observe.MetricModelCallCount, observe.MetricModelCallDuration,
		observe.MetricTokenUsage,
	} {
		if _, exists := names[name]; !exists {
			t.Fatalf("metric %q not exported; names=%v", name, names)
		}
	}
}

func TestObserverContainsParentContextPanic(t *testing.T) {
	t.Parallel()

	tracerProvider := sdktrace.NewTracerProvider()
	meterProvider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})
	observer, err := New(Options{
		TracerProvider: tracerProvider, MeterProvider: meterProvider,
		ParentContext: func(event.Event) context.Context { panic("parent boom") },
	})
	if err != nil {
		t.Fatal(err)
	}
	value := otelTestEvent(t, "run_parent", "start", event.RunStarted, 1, time.Now(), "", event.RunStartedPayload{Agent: "agent"})
	if err := observer.Sink(value); err == nil || !strings.Contains(err.Error(), "ParentContext panic") {
		t.Fatalf("Sink() error = %v", err)
	}
}

func TestNewRequiresApplicationOwnedProviders(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{}); err == nil {
		t.Fatal("New accepted missing Providers")
	}
}

func otelTestEvent(t *testing.T, runID, id string, eventType event.Type, sequence int64, createdAt time.Time, parentID string, payload any) event.Event {
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
