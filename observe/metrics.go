package observe

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

const (
	MetricRunCount             = "tiggy.agent.run.count"
	MetricRunDuration          = "tiggy.agent.run.duration"
	MetricTokenUsage           = "tiggy.agent.token.usage"
	MetricCost                 = "tiggy.agent.cost"
	MetricModelCallCount       = "tiggy.agent.model.call.count"
	MetricModelCallDuration    = "tiggy.agent.model.call.duration"
	MetricToolCallCount        = "tiggy.agent.tool.call.count"
	MetricToolCallDuration     = "tiggy.agent.tool.call.duration"
	MetricContextPrepareCount  = "tiggy.agent.context.prepare.count"
	MetricContextPrepareTime   = "tiggy.agent.context.prepare.duration"
	MetricCompressionCount     = "tiggy.agent.context.compression.count"
	MetricCompressionDuration  = "tiggy.agent.context.compression.duration"
	MetricArtifactOffloadCount = "tiggy.agent.artifact.offload.count"
	MetricArtifactOffloadTime  = "tiggy.agent.artifact.offload.duration"
	MetricMemoryOperationCount = "tiggy.agent.memory.operation.count"
	MetricMemoryOperationTime  = "tiggy.agent.memory.operation.duration"
	MetricRetryCount           = "tiggy.agent.model.retry.count"
	MetricFallbackCount        = "tiggy.agent.model.fallback.count"
	MetricLimitCount           = "tiggy.agent.limit.count"
)

// MetricKind 描述 Recorder 如何聚合 Measurement。
type MetricKind string

const (
	MetricCounter   MetricKind = "counter"
	MetricHistogram MetricKind = "histogram"
)

// Attribute 是有界基数的指标维度。
// Runtime/Event ID、错误码、Scope key 与自由文本 reason 有意排除。
type Attribute struct {
	Key   string
	Value string
}

// Measurement is an owned metric update derived from one lifecycle Event.
type Measurement struct {
	Name       string
	Kind       MetricKind
	Unit       string
	Value      float64
	Attributes []Attribute
}

func (m Measurement) Validate() error {
	if !strings.HasPrefix(m.Name, "tiggy.agent.") {
		return fmt.Errorf("metric name must use the tiggy.agent namespace")
	}
	if m.Kind != MetricCounter && m.Kind != MetricHistogram {
		return fmt.Errorf("unsupported metric kind %q", m.Kind)
	}
	if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) || m.Value < 0 {
		return fmt.Errorf("metric value must be finite and non-negative")
	}
	for index, attribute := range m.Attributes {
		if strings.TrimSpace(attribute.Key) == "" || strings.TrimSpace(attribute.Value) == "" {
			return fmt.Errorf("metric attribute %d requires key and value", index)
		}
	}
	return nil
}

func (m Measurement) Clone() Measurement {
	cloned := m
	cloned.Attributes = append([]Attribute(nil), m.Attributes...)
	return cloned
}

// Recorder receives metric updates synchronously. Implementations must be
// concurrency-safe and must copy a Measurement if they retain it.
type Recorder interface {
	Record(Measurement) error
}

type RecorderFunc func(Measurement) error

func (f RecorderFunc) Record(value Measurement) error {
	return f(value.Clone())
}

type runMeasurementState struct {
	started time.Time
	agent   string
}

type operationMeasurementState struct {
	runID     string
	started   time.Time
	startType event.Type
	attrs     []Attribute
}

// Metrics converts a complete per-Run Event stream into counters and duration
// histograms. One Metrics value may be shared by concurrent Runs.
type Metrics struct {
	mu         sync.Mutex
	recorder   Recorder
	runs       map[string]runMeasurementState
	operations map[string]operationMeasurementState
}

func NewMetrics(recorder Recorder) (*Metrics, error) {
	if recorder == nil {
		return nil, fmt.Errorf("metric recorder is required")
	}
	return &Metrics{
		recorder: recorder, runs: make(map[string]runMeasurementState),
		operations: make(map[string]operationMeasurementState),
	}, nil
}

// Sink consumes one Event. It expects each observed Run to begin with
// run.started so durations and stable Agent dimensions can be correlated.
func (m *Metrics) Sink(value event.Event) error {
	if m == nil {
		return fmt.Errorf("metrics observer is required")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	measurements, err := m.apply(value)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	for _, measurement := range measurements {
		if err := measurement.Validate(); err != nil {
			return err
		}
		if err := m.recorder.Record(measurement.Clone()); err != nil {
			return fmt.Errorf("record metric %q: %w", measurement.Name, err)
		}
	}
	return nil
}

func (m *Metrics) apply(value event.Event) ([]Measurement, error) {
	switch value.Type {
	case event.RunStarted:
		payload, err := decode[event.RunStartedPayload](value)
		if err != nil {
			return nil, err
		}
		if _, exists := m.runs[value.RunID]; exists {
			return nil, fmt.Errorf("run %q already started", value.RunID)
		}
		m.runs[value.RunID] = runMeasurementState{started: value.CreatedAt, agent: payload.Agent}
		return nil, nil
	case event.RunCompleted, event.RunFailed, event.RunCanceled, event.RunSuspended:
		return m.finishRun(value)
	case event.ModelCallStarted, event.ToolCallStarted, event.ContextPrepareStarted,
		event.CompressionStarted, event.ToolResultOffloadStarted,
		event.MemoryRecallStarted, event.MemoryObserveStarted:
		return nil, m.startOperation(value)
	case event.ModelCallCompleted, event.ToolCallCompleted, event.ContextPrepared,
		event.CompressionCompleted, event.CompressionFailed,
		event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed,
		event.MemoryRecallCompleted, event.MemoryObserveCompleted, event.MemoryObserveFailed:
		return m.finishOperation(value)
	case event.RetryScheduled:
		payload, err := decode[event.RetryScheduledPayload](value)
		if err != nil {
			return nil, err
		}
		return []Measurement{counter(MetricRetryCount, modelAttributes(payload.Model)...)}, nil
	case event.FallbackActivated:
		payload, err := decode[event.FallbackActivatedPayload](value)
		if err != nil {
			return nil, err
		}
		return []Measurement{counter(MetricFallbackCount,
			attribute("from.provider", payload.From.Provider),
			attribute("from.model", payload.From.Model),
			attribute("to.provider", payload.To.Provider),
			attribute("to.model", payload.To.Model),
		)}, nil
	case event.LimitReached:
		return []Measurement{counter(MetricLimitCount)}, nil
	case event.ModelCallDelta, event.ToolCallDelta, event.RuntimeError:
		return nil, nil
	default:
		// New Event types are compatible additions. Ignore them until a bounded
		// metric mapping is explicitly defined.
		return nil, nil
	}
}

func (m *Metrics) startOperation(value event.Event) error {
	if _, exists := m.runs[value.RunID]; !exists {
		return fmt.Errorf("operation %q belongs to an unstarted run", value.ID)
	}
	if _, exists := m.operations[value.ID]; exists {
		return fmt.Errorf("operation %q already started", value.ID)
	}
	attrs, err := operationStartAttributes(value)
	if err != nil {
		return err
	}
	m.operations[value.ID] = operationMeasurementState{
		runID: value.RunID, started: value.CreatedAt, startType: value.Type,
		attrs: append([]Attribute(nil), attrs...),
	}
	return nil
}

func (m *Metrics) finishOperation(value event.Event) ([]Measurement, error) {
	started, exists := m.operations[value.ParentID]
	if !exists || started.runID != value.RunID {
		return nil, fmt.Errorf("outcome %q has no matching operation", value.ID)
	}
	baseName, expected, status, outcomeAttrs, err := operationOutcome(value)
	if err != nil {
		return nil, err
	}
	if started.startType != expected {
		return nil, fmt.Errorf("outcome %q does not match %q", value.ID, started.startType)
	}
	duration := value.CreatedAt.Sub(started.started)
	if duration < 0 {
		return nil, fmt.Errorf("outcome %q precedes its start", value.ID)
	}
	delete(m.operations, value.ParentID)
	attrs := append(append([]Attribute(nil), started.attrs...), outcomeAttrs...)
	attrs = appendAttribute(attrs, "status", status)
	return []Measurement{
		counter(baseName+".count", attrs...),
		histogram(baseName+".duration", duration, attrs...),
	}, nil

}

func (m *Metrics) finishRun(value event.Event) ([]Measurement, error) {
	started, exists := m.runs[value.RunID]
	if !exists {
		return nil, fmt.Errorf("run %q has no matching start", value.RunID)
	}
	payload, err := decode[event.RunTerminalPayload](value)
	if err != nil {
		return nil, err
	}
	duration := value.CreatedAt.Sub(started.started)
	if duration < 0 {
		return nil, fmt.Errorf("run %q terminal event precedes its start", value.RunID)
	}
	delete(m.runs, value.RunID)
	for id, operation := range m.operations {
		if operation.runID == value.RunID {
			delete(m.operations, id)
		}
	}
	attrs := attributes(attribute("agent.name", started.agent), attribute("status", payload.Status))
	measurements := []Measurement{
		counter(MetricRunCount, attrs...),
		histogram(MetricRunDuration, duration, attrs...),
	}
	measurements = append(measurements, usageMeasurements(payload.Usage, started.agent)...)
	return measurements, nil
}

func operationStartAttributes(value event.Event) ([]Attribute, error) {
	switch value.Type {
	case event.ModelCallStarted:
		payload, err := decode[event.ModelCallPayload](value)
		return modelAttributes(payload.Model), err
	case event.ToolCallStarted:
		payload, err := decode[event.ToolCallPayload](value)
		return attributes(attribute("tool.name", payload.Name)), err
	case event.ContextPrepareStarted:
		payload, err := decode[event.ContextPreparePayload](value)
		return modelAttributes(payload.Model), err
	case event.CompressionStarted:
		payload, err := decode[event.CompressionPayload](value)
		return attributes(
			attribute("model.provider", payload.Model.Provider),
			attribute("model.name", payload.Model.Model),
			attribute("compression.strategy", payload.StrategyName),
		), err
	case event.ToolResultOffloadStarted:
		payload, err := decode[event.ToolResultOffloadPayload](value)
		return attributes(attribute("tool.name", payload.Name)), err
	case event.MemoryRecallStarted, event.MemoryObserveStarted:
		_, err := decode[event.MemoryPayload](value)
		operation := "recall"
		if value.Type == event.MemoryObserveStarted {
			operation = "observe"
		}
		return attributes(attribute("memory.operation", operation)), err
	default:
		return nil, fmt.Errorf("unsupported operation start %q", value.Type)
	}
}

func operationOutcome(value event.Event) (string, event.Type, string, []Attribute, error) {
	switch value.Type {
	case event.ModelCallCompleted:
		payload, err := decode[event.ModelCallPayload](value)
		return "tiggy.agent.model.call", event.ModelCallStarted, payload.Status,
			attributes(attribute("error.class", errorClass(payload.Error))), err
	case event.ToolCallCompleted:
		payload, err := decode[event.ToolCallPayload](value)
		return "tiggy.agent.tool.call", event.ToolCallStarted, payload.Status,
			attributes(attribute("tool.is_error", fmt.Sprintf("%t", payload.IsError)), attribute("error.class", errorClass(payload.Error))), err
	case event.ContextPrepared:
		payload, err := decode[event.ContextPreparePayload](value)
		return "tiggy.agent.context.prepare", event.ContextPrepareStarted, payload.Status,
			attributes(attribute("context.pressure", payload.Pressure), attribute("error.class", errorClass(payload.Error))), err
	case event.CompressionCompleted, event.CompressionFailed:
		payload, err := decode[event.CompressionPayload](value)
		return "tiggy.agent.context.compression", event.CompressionStarted, payload.Status,
			attributes(attribute("error.class", errorClass(payload.Error))), err
	case event.ToolResultOffloadCompleted, event.ToolResultOffloadFailed:
		payload, err := decode[event.ToolResultOffloadPayload](value)
		return "tiggy.agent.artifact.offload", event.ToolResultOffloadStarted, payload.Status,
			attributes(attribute("error.class", errorClass(payload.Error))), err
	case event.MemoryRecallCompleted:
		payload, err := decode[event.MemoryPayload](value)
		return "tiggy.agent.memory.operation", event.MemoryRecallStarted, payload.Status,
			attributes(attribute("error.class", errorClass(payload.Error))), err
	case event.MemoryObserveCompleted, event.MemoryObserveFailed:
		payload, err := decode[event.MemoryPayload](value)
		return "tiggy.agent.memory.operation", event.MemoryObserveStarted, payload.Status,
			attributes(attribute("error.class", errorClass(payload.Error))), err
	default:
		return "", "", "", nil, fmt.Errorf("unsupported operation outcome %q", value.Type)
	}
}

func usageMeasurements(usage model.Usage, agentName string) []Measurement {
	base := attributes(attribute("agent.name", agentName), attribute("usage.source", string(usage.Source)))
	values := []struct {
		kind  string
		value int64
	}{
		{kind: "input", value: usage.InputTokens},
		{kind: "output", value: usage.OutputTokens},
		{kind: "cached_input", value: usage.CachedInputTokens},
		{kind: "reasoning", value: usage.ReasoningTokens},
	}
	result := make([]Measurement, 0, len(values)+1)
	for _, value := range values {
		if value.value == 0 {
			continue
		}
		attrs := append(append([]Attribute(nil), base...), attribute("token.type", value.kind))
		result = append(result, Measurement{
			Name: MetricTokenUsage, Kind: MetricCounter, Unit: "{token}", Value: float64(value.value), Attributes: attrs,
		})
	}
	if usage.CostMicros > 0 {
		result = append(result, Measurement{
			Name: MetricCost, Kind: MetricCounter, Unit: "{USD_micro}", Value: float64(usage.CostMicros), Attributes: base,
		})
	}
	return result
}

func counter(name string, attrs ...Attribute) Measurement {
	return Measurement{Name: name, Kind: MetricCounter, Unit: "{event}", Value: 1, Attributes: attributes(attrs...)}
}

func histogram(name string, duration time.Duration, attrs ...Attribute) Measurement {
	return Measurement{
		Name: name, Kind: MetricHistogram, Unit: "ms",
		Value: float64(duration) / float64(time.Millisecond), Attributes: attributes(attrs...),
	}
}

func modelAttributes(identity model.Identity) []Attribute {
	return attributes(
		attribute("model.provider", identity.Provider),
		attribute("model.name", identity.Model),
		attribute("model.revision", identity.Revision),
	)
}

func attribute(key, value string) Attribute {
	return Attribute{Key: key, Value: value}
}

func attributes(values ...Attribute) []Attribute {
	result := make([]Attribute, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func appendAttribute(values []Attribute, key, value string) []Attribute {
	if strings.TrimSpace(value) == "" {
		return values
	}
	return append(values, attribute(key, value))
}

func errorClass(value *event.ErrorPayload) string {
	if value == nil {
		return ""
	}
	return value.Class
}
