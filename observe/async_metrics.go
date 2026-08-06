package observe

import (
	"fmt"
	"strings"
	"sync"
)

// AsyncMetricsOptions configures the bounded metric identity of one AsyncSink.
// Name is required and becomes the observer.name attribute.
type AsyncMetricsOptions struct {
	Name string
}

// AsyncMetrics samples one AsyncSink into the backend-neutral Recorder contract.
// It owns no timer; the application decides when Record is called.
type AsyncMetrics struct {
	mu       sync.Mutex
	sink     *AsyncSink
	recorder Recorder
	attrs    []Attribute
	last     AsyncStats
}

// NewAsyncMetrics binds one AsyncSink to a Recorder.
func NewAsyncMetrics(sink *AsyncSink, recorder Recorder, options AsyncMetricsOptions) (*AsyncMetrics, error) {
	if sink == nil {
		return nil, fmt.Errorf("async sink is required")
	}
	if recorder == nil {
		return nil, fmt.Errorf("metric recorder is required")
	}
	name := strings.TrimSpace(options.Name)
	if err := validateAsyncMetricName(name); err != nil {
		return nil, err
	}
	return &AsyncMetrics{
		sink:     sink,
		recorder: recorder,
		attrs:    []Attribute{{Key: "observer.name", Value: name}},
	}, nil
}

// Record emits current gauges and monotonic counter deltas since the prior call.
// The baseline advances before external Recorder calls, so a partial Recorder failure
// is not retried automatically and cannot duplicate already accepted counter deltas.
func (m *AsyncMetrics) Record() error {
	if m == nil {
		return fmt.Errorf("async metrics is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.sink.Stats()
	measurements, err := asyncMeasurements(current, m.last, m.attrs)
	if err != nil {
		return err
	}
	m.last = current
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

func asyncMeasurements(current, previous AsyncStats, attrs []Attribute) ([]Measurement, error) {
	type counterValue struct {
		name     string
		unit     string
		current  uint64
		previous uint64
		attrs    []Attribute
	}
	counters := []counterValue{
		{name: MetricAsyncSubmitted, unit: "{event}", current: current.Submitted, previous: previous.Submitted},
		{name: MetricAsyncProcessed, unit: "{event}", current: current.Processed, previous: previous.Processed},
		{name: MetricAsyncDelegateCalls, unit: "{call}", current: current.DelegateCalls, previous: previous.DelegateCalls},
		{name: MetricAsyncDropped, unit: "{event}", current: current.DroppedNewest, previous: previous.DroppedNewest, attrs: []Attribute{{Key: "reason", Value: "newest"}}},
		{name: MetricAsyncDropped, unit: "{event}", current: current.DroppedOldest, previous: previous.DroppedOldest, attrs: []Attribute{{Key: "reason", Value: "oldest"}}},
		{name: MetricAsyncDropped, unit: "{event}", current: current.Discarded, previous: previous.Discarded, attrs: []Attribute{{Key: "reason", Value: "abort"}}},
		{name: MetricAsyncDelegateFailure, unit: "{call}", current: current.DelegateFailures, previous: previous.DelegateFailures},
		{name: MetricAsyncRejected, unit: "{event}", current: current.Rejected, previous: previous.Rejected},
	}
	result := []Measurement{
		gauge(MetricAsyncQueueCapacity, "{event}", float64(current.QueueCapacity), attrs...),
		gauge(MetricAsyncBatchSize, "{event}", float64(current.BatchSize), attrs...),
		gauge(MetricAsyncQueueDepth, "{event}", float64(current.QueueDepth), attrs...),
		gauge(MetricAsyncPending, "{event}", float64(current.Pending), attrs...),
		gauge(MetricAsyncClosed, "1", boolValue(current.Closed), attrs...),
		gauge(MetricAsyncAborted, "1", boolValue(current.Aborted), attrs...),
	}
	for _, value := range counters {
		delta, err := monotonicDelta(value.name, value.current, value.previous)
		if err != nil {
			return nil, err
		}
		if delta == 0 {
			continue
		}
		counterAttrs := append(append([]Attribute(nil), attrs...), value.attrs...)
		result = append(result, Measurement{
			Name: value.name, Kind: MetricCounter, Unit: value.unit,
			Value: float64(delta), Attributes: counterAttrs,
		})
	}
	return result, nil
}

func monotonicDelta(name string, current, previous uint64) (uint64, error) {
	if current < previous {
		return 0, fmt.Errorf("async metric %q regressed from %d to %d", name, previous, current)
	}
	return current - previous, nil
}

func gauge(name, unit string, value float64, attrs ...Attribute) Measurement {
	return Measurement{
		Name: name, Kind: MetricGauge, Unit: unit, Value: value,
		Attributes: attributes(attrs...),
	}
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func validateAsyncMetricName(value string) error {
	if value == "" {
		return fmt.Errorf("async metric observer name is required")
	}
	if len(value) > 64 {
		return fmt.Errorf("async metric observer name exceeds 64 bytes")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("async metric observer name contains unsupported character %q", char)
	}
	return nil
}
