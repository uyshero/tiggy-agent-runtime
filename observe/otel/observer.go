// Package otel 将 Tiggy 生命周期事件适配为 OpenTelemetry 追踪与指标。
// Provider / Reader / Exporter 与关闭生命周期由应用方拥有。
package otel

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/observe"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/uyshero/tiggy-agent-runtime/observe/otel"

// ParentContext 解析新 Run span 的父上下文。
// 接收一份自有的 run.started Event，且必须并发安全；返回 nil 无效。
type ParentContext func(event.Event) context.Context

// Options 注入 Tracer/Meter Provider 与可选父上下文解析。
type Options struct {
	TracerProvider oteltrace.TracerProvider
	MeterProvider  otelmetric.MeterProvider
	ParentContext  ParentContext
}

// Observer 把一个或多个完整 Run 事件流转为 OTel span 与指标。
// 并发安全，不拥有也不关闭 Providers。
type Observer struct {
	trace          *traceObserver
	metrics        *observe.Metrics
	metricRecorder *metricRecorder
}

// New 构造 Observer；TracerProvider 与 MeterProvider 必填。
func New(options Options) (*Observer, error) {
	if options.TracerProvider == nil || options.MeterProvider == nil {
		return nil, fmt.Errorf("TracerProvider and MeterProvider are required")
	}
	recorder, err := newMetricRecorder(options.MeterProvider.Meter(instrumentationName))
	if err != nil {
		return nil, err
	}
	metrics, err := observe.NewMetrics(recorder)
	if err != nil {
		return nil, err
	}
	return &Observer{
		trace: &traceObserver{
			tracer: options.TracerProvider.Tracer(instrumentationName), parentContext: options.ParentContext,
			runs: make(map[string]string), spans: make(map[string]spanState),
		},
		metrics:        metrics,
		metricRecorder: recorder,
	}, nil
}

// Sink implements the event.Sink callback shape. Wrap it with
// observe.BestEffort when telemetry failure must not change Run execution.
func (o *Observer) Sink(value event.Event) error {
	if o == nil {
		return fmt.Errorf("OpenTelemetry observer is required")
	}
	if err := o.trace.Sink(cloneEvent(value)); err != nil {
		return err
	}
	return o.metrics.Sink(cloneEvent(value))
}

// NewAsyncMetrics binds an AsyncSink sampler to this Observer's application-owned MeterProvider.
// The returned sampler owns no timer; applications call Record at their chosen interval.
func (o *Observer) NewAsyncMetrics(name string, sink *observe.AsyncSink) (*observe.AsyncMetrics, error) {
	if o == nil {
		return nil, fmt.Errorf("OpenTelemetry observer is required")
	}
	return observe.NewAsyncMetrics(sink, o.metricRecorder, observe.AsyncMetricsOptions{Name: name})
}

type metricRecorder struct {
	counters   map[string]otelmetric.Float64Counter
	histograms map[string]otelmetric.Float64Histogram
	gauges     map[string]otelmetric.Float64Gauge
}

type instrumentSpec struct {
	name string
	kind observe.MetricKind
	unit string
}

func newMetricRecorder(meter otelmetric.Meter) (*metricRecorder, error) {
	specs := []instrumentSpec{
		{name: observe.MetricRunCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricRunDuration, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricTokenUsage, kind: observe.MetricCounter, unit: "{token}"},
		{name: observe.MetricCost, kind: observe.MetricCounter, unit: "{USD_micro}"},
		{name: observe.MetricModelCallCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricModelCallDuration, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricToolCallCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricToolCallDuration, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricContextPrepareCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricContextPrepareTime, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricCompressionCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricCompressionDuration, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricArtifactOffloadCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricArtifactOffloadTime, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricMemoryOperationCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricMemoryOperationTime, kind: observe.MetricHistogram, unit: "ms"},
		{name: observe.MetricRetryCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricFallbackCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricLimitCount, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricAsyncQueueCapacity, kind: observe.MetricGauge, unit: "{event}"},
		{name: observe.MetricAsyncBatchSize, kind: observe.MetricGauge, unit: "{event}"},
		{name: observe.MetricAsyncQueueDepth, kind: observe.MetricGauge, unit: "{event}"},
		{name: observe.MetricAsyncPending, kind: observe.MetricGauge, unit: "{event}"},
		{name: observe.MetricAsyncSubmitted, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricAsyncProcessed, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricAsyncDelegateCalls, kind: observe.MetricCounter, unit: "{call}"},
		{name: observe.MetricAsyncDropped, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricAsyncDelegateFailure, kind: observe.MetricCounter, unit: "{call}"},
		{name: observe.MetricAsyncRejected, kind: observe.MetricCounter, unit: "{event}"},
		{name: observe.MetricAsyncClosed, kind: observe.MetricGauge, unit: "1"},
		{name: observe.MetricAsyncAborted, kind: observe.MetricGauge, unit: "1"},
	}
	value := &metricRecorder{
		counters:   make(map[string]otelmetric.Float64Counter),
		histograms: make(map[string]otelmetric.Float64Histogram),
		gauges:     make(map[string]otelmetric.Float64Gauge),
	}
	for _, spec := range specs {
		switch spec.kind {
		case observe.MetricCounter:
			instrument, err := meter.Float64Counter(spec.name, otelmetric.WithUnit(spec.unit))
			if err != nil {
				return nil, fmt.Errorf("create counter %q: %w", spec.name, err)
			}
			value.counters[spec.name] = instrument
		case observe.MetricHistogram:
			instrument, err := meter.Float64Histogram(spec.name, otelmetric.WithUnit(spec.unit))
			if err != nil {
				return nil, fmt.Errorf("create histogram %q: %w", spec.name, err)
			}
			value.histograms[spec.name] = instrument
		case observe.MetricGauge:
			instrument, err := meter.Float64Gauge(spec.name, otelmetric.WithUnit(spec.unit))
			if err != nil {
				return nil, fmt.Errorf("create gauge %q: %w", spec.name, err)
			}
			value.gauges[spec.name] = instrument
		}
	}
	return value, nil
}

func (r *metricRecorder) Record(value observe.Measurement) error {
	attrs := make([]attribute.KeyValue, len(value.Attributes))
	for index, item := range value.Attributes {
		attrs[index] = attribute.String(item.Key, item.Value)
	}
	options := otelmetric.WithAttributes(attrs...)
	switch value.Kind {
	case observe.MetricCounter:
		instrument, exists := r.counters[value.Name]
		if !exists {
			return fmt.Errorf("unknown counter %q", value.Name)
		}
		instrument.Add(context.Background(), value.Value, options)
	case observe.MetricHistogram:
		instrument, exists := r.histograms[value.Name]
		if !exists {
			return fmt.Errorf("unknown histogram %q", value.Name)
		}
		instrument.Record(context.Background(), value.Value, options)
	case observe.MetricGauge:
		instrument, exists := r.gauges[value.Name]
		if !exists {
			return fmt.Errorf("unknown gauge %q", value.Name)
		}
		instrument.Record(context.Background(), value.Value, options)
	default:
		return fmt.Errorf("unsupported metric kind %q", value.Kind)
	}
	return nil
}

type spanState struct {
	runID     string
	startType event.Type
	span      oteltrace.Span
}

type traceObserver struct {
	mu            sync.Mutex
	tracer        oteltrace.Tracer
	parentContext ParentContext
	runs          map[string]string
	spans         map[string]spanState
}

func (o *traceObserver) Sink(value event.Event) error {
	if err := value.Validate(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	switch value.Type {
	case event.RunStarted:
		return o.startRun(value)
	case event.RunCompleted, event.RunFailed, event.RunCanceled, event.RunSuspended:
		return o.finishRun(value)
	case event.ModelCallStarted, event.ToolCallStarted, event.ContextPrepareStarted,
		event.CompressionStarted, event.ToolResultOffloadStarted,
		event.MemoryRecallStarted, event.MemoryObserveStarted:
		return o.startOperation(value)
	case event.ModelCallCompleted, event.ToolCallCompleted, event.ContextPrepared,
		event.CompressionCompleted, event.CompressionFailed,
		event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed,
		event.MemoryRecallCompleted, event.MemoryObserveCompleted, event.MemoryObserveFailed:
		return o.finishOperation(value)
	case event.RuntimeError, event.RetryScheduled, event.FallbackActivated, event.LimitReached,
		event.ApprovalRequired, event.ApprovalResolved:
		return o.addRunEvent(value)
	case event.ModelCallDelta, event.ToolCallDelta:
		return nil
	default:
		// Unknown Event payloads are not attached because their privacy contract
		// is not known. Event type additions remain forward-compatible.
		return nil
	}
}

func (o *traceObserver) startRun(value event.Event) error {
	if _, exists := o.runs[value.RunID]; exists {
		return fmt.Errorf("run %q already started", value.RunID)
	}
	payload, err := decode[event.RunStartedPayload](value)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if o.parentContext != nil {
		ctx, err = callParentContext(o.parentContext, value)
		if err != nil {
			return err
		}
	}
	_, span := o.tracer.Start(ctx, "tiggy.agent.run",
		oteltrace.WithTimestamp(value.CreatedAt),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(
			attribute.String("tiggy.run.id", value.RunID),
			attribute.String("tiggy.agent.name", payload.Agent),
		),
	)
	o.runs[value.RunID] = value.ID
	o.spans[value.ID] = spanState{runID: value.RunID, startType: value.Type, span: span}
	return nil
}

func (o *traceObserver) startOperation(value event.Event) error {
	rootID, exists := o.runs[value.RunID]
	if !exists {
		return fmt.Errorf("operation %q belongs to an unstarted run", value.ID)
	}
	if _, exists := o.spans[value.ID]; exists {
		return fmt.Errorf("operation %q already started", value.ID)
	}
	parent := o.spans[rootID].span
	if candidate, exists := o.spans[value.ParentID]; exists {
		parent = candidate.span
	}
	attrs, err := traceStartAttributes(value)
	if err != nil {
		return err
	}
	attrs = append(attrs,
		attribute.String("tiggy.run.id", value.RunID),
		attribute.String("tiggy.event.start_id", value.ID),
	)
	ctx := oteltrace.ContextWithSpan(context.Background(), parent)
	_, span := o.tracer.Start(ctx, operationSpanName(value.Type),
		oteltrace.WithTimestamp(value.CreatedAt),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(attrs...),
	)
	o.spans[value.ID] = spanState{runID: value.RunID, startType: value.Type, span: span}
	return nil
}

func (o *traceObserver) finishOperation(value event.Event) error {
	started, exists := o.spans[value.ParentID]
	if !exists || started.runID != value.RunID {
		return fmt.Errorf("outcome %q has no matching span", value.ID)
	}
	expected, attrs, failed, err := traceOutcome(value)
	if err != nil {
		return err
	}
	if started.startType != expected {
		return fmt.Errorf("outcome %q does not match %q", value.ID, started.startType)
	}
	started.span.SetAttributes(attrs...)
	if failed {
		started.span.SetStatus(codes.Error, "operation failed")
	}
	started.span.End(oteltrace.WithTimestamp(value.CreatedAt))
	delete(o.spans, value.ParentID)
	return nil
}

func (o *traceObserver) finishRun(value event.Event) error {
	rootID, exists := o.runs[value.RunID]
	if !exists {
		return fmt.Errorf("run %q has no matching span", value.RunID)
	}
	payload, err := decode[event.RunTerminalPayload](value)
	if err != nil {
		return err
	}
	for id, active := range o.spans {
		if active.runID == value.RunID && id != rootID {
			active.span.SetAttributes(attribute.Bool("tiggy.operation.incomplete", true))
			active.span.SetStatus(codes.Error, "operation incomplete at Run termination")
			active.span.End(oteltrace.WithTimestamp(value.CreatedAt))
			delete(o.spans, id)
		}
	}
	root := o.spans[rootID].span
	root.SetAttributes(
		attribute.String("tiggy.run.status", payload.Status),
		attribute.Int("tiggy.run.iterations", payload.Iterations),
		attribute.Int64("tiggy.usage.input_tokens", payload.Usage.InputTokens),
		attribute.Int64("tiggy.usage.output_tokens", payload.Usage.OutputTokens),
		attribute.Int64("tiggy.usage.reasoning_tokens", payload.Usage.ReasoningTokens),
	)
	if payload.Error != nil {
		root.SetAttributes(
			attribute.String("error.type", payload.Error.Class),
			attribute.String("tiggy.error.code", payload.Error.Code),
		)
	}
	if payload.Status != "completed" && payload.Status != "suspended" {
		root.SetStatus(codes.Error, payload.Status)
	}
	root.End(oteltrace.WithTimestamp(value.CreatedAt))
	delete(o.spans, rootID)
	delete(o.runs, value.RunID)
	return nil
}

func (o *traceObserver) addRunEvent(value event.Event) error {
	rootID, exists := o.runs[value.RunID]
	if !exists {
		return fmt.Errorf("event %q belongs to an unstarted run", value.ID)
	}
	attrs, err := traceDiagnosticAttributes(value)
	if err != nil {
		return err
	}
	o.spans[rootID].span.AddEvent(string(value.Type),
		oteltrace.WithTimestamp(value.CreatedAt), oteltrace.WithAttributes(attrs...),
	)
	return nil
}

func traceStartAttributes(value event.Event) ([]attribute.KeyValue, error) {
	switch value.Type {
	case event.ModelCallStarted:
		payload, err := decode[event.ModelCallPayload](value)
		return []attribute.KeyValue{
			attribute.String("gen_ai.provider.name", payload.Model.Provider),
			attribute.String("gen_ai.request.model", payload.Model.Model),
			attribute.Int("tiggy.model.attempt", payload.Attempt),
		}, err
	case event.ToolCallStarted:
		payload, err := decode[event.ToolCallPayload](value)
		return []attribute.KeyValue{attribute.String("gen_ai.tool.name", payload.Name)}, err
	case event.ContextPrepareStarted:
		payload, err := decode[event.ContextPreparePayload](value)
		return []attribute.KeyValue{
			attribute.String("gen_ai.provider.name", payload.Model.Provider),
			attribute.String("gen_ai.request.model", payload.Model.Model),
		}, err
	case event.CompressionStarted:
		payload, err := decode[event.CompressionPayload](value)
		return []attribute.KeyValue{
			attribute.String("gen_ai.provider.name", payload.Model.Provider),
			attribute.String("gen_ai.request.model", payload.Model.Model),
			attribute.String("tiggy.compression.strategy", payload.StrategyName),
			attribute.String("tiggy.compression.strategy_version", payload.StrategyVersion),
		}, err
	case event.ToolResultOffloadStarted:
		payload, err := decode[event.ToolResultOffloadPayload](value)
		return []attribute.KeyValue{
			attribute.String("gen_ai.tool.name", payload.Name),
			attribute.Int64("tiggy.artifact.source_bytes", payload.SourceBytes),
		}, err
	case event.MemoryRecallStarted, event.MemoryObserveStarted:
		payload, err := decode[event.MemoryPayload](value)
		operation := "recall"
		if value.Type == event.MemoryObserveStarted {
			operation = "observe"
		}
		return []attribute.KeyValue{
			attribute.String("tiggy.memory.operation", operation),
			attribute.String("tiggy.memory.namespace", payload.Namespace),
		}, err
	default:
		return nil, fmt.Errorf("unsupported operation start %q", value.Type)
	}
}

func traceOutcome(value event.Event) (event.Type, []attribute.KeyValue, bool, error) {
	switch value.Type {
	case event.ModelCallCompleted:
		payload, err := decode[event.ModelCallPayload](value)
		return event.ModelCallStarted, outcomeAttributes(payload.Status, payload.Error), payload.Status != "completed", err
	case event.ToolCallCompleted:
		payload, err := decode[event.ToolCallPayload](value)
		attrs := append(outcomeAttributes(payload.Status, payload.Error), attribute.Bool("tiggy.tool.is_error", payload.IsError))
		return event.ToolCallStarted, attrs, payload.Status != "completed", err
	case event.ContextPrepared:
		payload, err := decode[event.ContextPreparePayload](value)
		attrs := append(outcomeAttributes(payload.Status, payload.Error),
			attribute.String("tiggy.context.pressure", payload.Pressure),
			attribute.Int64("tiggy.context.required_tokens", payload.RequiredTokens),
		)
		return event.ContextPrepareStarted, attrs, payload.Status != "ready", err
	case event.CompressionCompleted, event.CompressionFailed:
		payload, err := decode[event.CompressionPayload](value)
		attrs := append(outcomeAttributes(payload.Status, payload.Error),
			attribute.Int64("tiggy.compression.before_input_tokens", payload.BeforeInputTokens),
			attribute.Int64("tiggy.compression.after_input_tokens", payload.AfterInputTokens),
		)
		return event.CompressionStarted, attrs, value.Type == event.CompressionFailed, err
	case event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed:
		payload, err := decode[event.ToolResultOffloadPayload](value)
		return event.ToolResultOffloadStarted, outcomeAttributes(payload.Status, payload.Error), value.Type == event.ToolResultOffloadFailed, err
	case event.MemoryRecallCompleted:
		payload, err := decode[event.MemoryPayload](value)
		return event.MemoryRecallStarted, outcomeAttributes(payload.Status, payload.Error), payload.Status == "failed", err
	case event.MemoryObserveCompleted, event.MemoryObserveFailed:
		payload, err := decode[event.MemoryPayload](value)
		return event.MemoryObserveStarted, outcomeAttributes(payload.Status, payload.Error), value.Type == event.MemoryObserveFailed && payload.Status != "degraded", err
	default:
		return "", nil, false, fmt.Errorf("unsupported operation outcome %q", value.Type)
	}
}

func traceDiagnosticAttributes(value event.Event) ([]attribute.KeyValue, error) {
	switch value.Type {
	case event.RuntimeError:
		payload, err := decode[event.ErrorPayload](value)
		return []attribute.KeyValue{
			attribute.String("error.type", payload.Class),
			attribute.String("tiggy.error.code", payload.Code),
			attribute.Bool("tiggy.error.retryable", payload.Retryable),
		}, err
	case event.RetryScheduled:
		payload, err := decode[event.RetryScheduledPayload](value)
		return []attribute.KeyValue{
			attribute.String("gen_ai.provider.name", payload.Model.Provider),
			attribute.String("gen_ai.request.model", payload.Model.Model),
			attribute.Int("tiggy.retry.next_attempt", payload.NextAttempt),
			attribute.Int64("tiggy.retry.delay_millis", payload.DelayMillis),
		}, err
	case event.FallbackActivated:
		payload, err := decode[event.FallbackActivatedPayload](value)
		return []attribute.KeyValue{
			attribute.String("tiggy.fallback.from_provider", payload.From.Provider),
			attribute.String("tiggy.fallback.from_model", payload.From.Model),
			attribute.String("tiggy.fallback.to_provider", payload.To.Provider),
			attribute.String("tiggy.fallback.to_model", payload.To.Model),
			attribute.Int("tiggy.fallback.index", payload.FallbackIndex),
		}, err
	case event.LimitReached:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported diagnostic event %q", value.Type)
	}
}

func outcomeAttributes(status string, payload *event.ErrorPayload) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.String("tiggy.operation.status", status)}
	if payload != nil {
		attrs = append(attrs,
			attribute.String("error.type", payload.Class),
			attribute.String("tiggy.error.code", payload.Code),
			attribute.Bool("tiggy.error.retryable", payload.Retryable),
		)
	}
	return attrs
}

func operationSpanName(eventType event.Type) string {
	switch eventType {
	case event.ModelCallStarted:
		return "tiggy.agent.model.call"
	case event.ToolCallStarted:
		return "tiggy.agent.tool.call"
	case event.ContextPrepareStarted:
		return "tiggy.agent.context.prepare"
	case event.CompressionStarted:
		return "tiggy.agent.context.compression"
	case event.ToolResultOffloadStarted:
		return "tiggy.agent.artifact.offload"
	case event.MemoryRecallStarted:
		return "tiggy.agent.memory.recall"
	case event.MemoryObserveStarted:
		return "tiggy.agent.memory.observe"
	default:
		return "tiggy.agent.operation"
	}
}

func callParentContext(resolver ParentContext, value event.Event) (ctx context.Context, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("OpenTelemetry ParentContext panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	ctx = resolver(cloneEvent(value))
	if ctx == nil {
		return nil, fmt.Errorf("OpenTelemetry ParentContext returned nil")
	}
	return ctx, nil
}

func decode[T any](value event.Event) (T, error) {
	var payload T
	if err := json.Unmarshal(value.Payload, &payload); err != nil {
		return payload, fmt.Errorf("decode %s payload: %w", value.Type, err)
	}
	return payload, nil
}

func cloneEvent(value event.Event) event.Event {
	cloned := value
	cloned.Payload = append(json.RawMessage(nil), value.Payload...)
	return cloned
}
