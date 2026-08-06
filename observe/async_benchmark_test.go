package observe

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func BenchmarkDirectSink(b *testing.B) {
	value := benchmarkEvent(b)
	var processed uint64
	sink := func(event.Event) error {
		processed++
		return nil
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := sink(value); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if processed != uint64(b.N) {
		b.Fatalf("processed = %d, want %d", processed, b.N)
	}
}

func BenchmarkAsyncSink(b *testing.B) {
	value := benchmarkEvent(b)
	var processed uint64
	async, err := NewAsyncSink(func(event.Event) error {
		processed++
		return nil
	}, AsyncOptions{
		QueueSize:  4096,
		DropPolicy: AsyncBlock,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := async.Sink(value); err != nil {
			b.Fatal(err)
		}
	}
	if err := async.Shutdown(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if processed != uint64(b.N) {
		b.Fatalf("processed = %d, want %d", processed, b.N)
	}
}

func BenchmarkAsyncBatchSink(b *testing.B) {
	value := benchmarkEvent(b)
	var processed uint64
	async, err := NewAsyncBatchSink(func(values []event.Event) error {
		processed += uint64(len(values))
		return nil
	}, AsyncBatchOptions{
		QueueSize:  4096,
		BatchSize:  64,
		DropPolicy: AsyncBlock,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := async.Sink(value); err != nil {
			b.Fatal(err)
		}
	}
	if err := async.Shutdown(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if processed != uint64(b.N) {
		b.Fatalf("processed = %d, want %d", processed, b.N)
	}
}

func BenchmarkAsyncSinkParallel(b *testing.B) {
	value := benchmarkEvent(b)
	async, err := NewAsyncSink(func(event.Event) error {
		return nil
	}, AsyncOptions{
		QueueSize:  4096,
		DropPolicy: AsyncBlock,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := async.Sink(value); err != nil {
				b.Error(err)
				return
			}
		}
	})
	if err := async.Shutdown(context.Background()); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkAsyncBatchSinkParallel(b *testing.B) {
	value := benchmarkEvent(b)
	async, err := NewAsyncBatchSink(func([]event.Event) error {
		return nil
	}, AsyncBatchOptions{
		QueueSize:  4096,
		BatchSize:  64,
		DropPolicy: AsyncBlock,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := async.Sink(value); err != nil {
				b.Error(err)
				return
			}
		}
	})
	if err := async.Shutdown(context.Background()); err != nil {
		b.Fatal(err)
	}
}

func benchmarkEvent(b *testing.B) event.Event {
	b.Helper()
	payload, err := json.Marshal(event.ModelDeltaPayload{
		CallID: "call_1",
		Delta:  model.Delta{Type: model.DeltaText, Text: "delta"},
	})
	if err != nil {
		b.Fatal(err)
	}
	value := event.Event{
		ProtocolVersion: event.ProtocolV1,
		ID:              "evt_1",
		RunID:           "bench",
		Type:            event.ModelCallDelta,
		Sequence:        1,
		CreatedAt:       time.Now(),
		Payload:         payload,
	}
	if err := value.Validate(); err != nil {
		b.Fatal(err)
	}
	return value
}
