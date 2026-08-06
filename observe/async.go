package observe

import (
	"context"
	"fmt"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

type AsyncDropPolicy string

const (
	// AsyncDropNewest drops the event currently being submitted when the queue is full.
	// Sink still returns nil because the caller explicitly chose best-effort telemetry semantics.
	AsyncDropNewest AsyncDropPolicy = "drop_newest"
	// AsyncDropOldest removes one queued event when the queue is full and attempts to enqueue the newest event.
	// This favors fresher operational telemetry over older queued telemetry.
	AsyncDropOldest AsyncDropPolicy = "drop_oldest"
	// AsyncBlock waits for queue capacity. It preserves accepted events but can still backpressure the Run.
	AsyncBlock AsyncDropPolicy = "block"
)

// AsyncOptions configures an AsyncSink.
// QueueSize defaults to 1024. DropPolicy defaults to AsyncDropNewest.
// OnError receives delegate failures, delegate panics, and drop diagnostics; it must return quickly.
type AsyncOptions struct {
	QueueSize  int
	DropPolicy AsyncDropPolicy
	OnError    ErrorHandler
}

// AsyncSink wraps a synchronous Sink with a bounded single-worker queue.
//
// It is intended for operational logging and telemetry that should not run inline with Agent execution.
// Strict audit and required recording should normally use a direct synchronous Sink instead.
type AsyncSink struct {
	sink       event.Sink
	queue      chan event.Event
	dropPolicy AsyncDropPolicy
	onError    ErrorHandler

	mu       sync.Mutex
	closed   bool
	pending  int
	sending  int
	zero     chan struct{}
	noSend   chan struct{}
	closing  chan struct{}
	done     chan struct{}
	doneOnce sync.Once
	stopped  chan struct{}
}

// NewAsyncSink starts an AsyncSink worker around sink.
func NewAsyncSink(sink event.Sink, options AsyncOptions) (*AsyncSink, error) {
	if sink == nil {
		return nil, fmt.Errorf("async sink delegate is required")
	}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = 1024
	}
	dropPolicy := options.DropPolicy
	if dropPolicy == "" {
		dropPolicy = AsyncDropNewest
	}
	switch dropPolicy {
	case AsyncDropNewest, AsyncDropOldest, AsyncBlock:
	default:
		return nil, fmt.Errorf("unsupported async sink drop policy %q", dropPolicy)
	}
	value := &AsyncSink{
		sink:       sink,
		queue:      make(chan event.Event, queueSize),
		dropPolicy: dropPolicy,
		onError:    options.OnError,
		zero:       closedChannel(),
		noSend:     closedChannel(),
		closing:    make(chan struct{}),
		done:       make(chan struct{}),
		stopped:    make(chan struct{}),
	}
	go value.run()
	return value, nil
}

// Sink accepts an owned Event copy for asynchronous delivery.
// Drop policies report queue overflow through OnError and return nil; Sink after Shutdown returns an error.
func (a *AsyncSink) Sink(value event.Event) error {
	if a == nil {
		return fmt.Errorf("async sink is required")
	}
	cloned := cloneEvent(value)
	switch a.dropPolicy {
	case AsyncBlock:
		return a.enqueueBlocking(cloned)
	case AsyncDropOldest:
		return a.enqueueDropOldest(cloned)
	default:
		return a.enqueueDropNewest(cloned)
	}
}

// Flush waits until all currently accepted events have been delivered to the delegate Sink.
// It does not close the AsyncSink and does not prevent later Sink calls from accepting more events.
func (a *AsyncSink) Flush(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("async sink is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		a.mu.Lock()
		if a.pending == 0 {
			a.mu.Unlock()
			return nil
		}
		zero := a.zero
		a.mu.Unlock()
		select {
		case <-zero:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Shutdown stops accepting new events, drains accepted events, and waits for the worker to exit.
// If ctx expires, a later Shutdown call may continue waiting for the same worker.
func (a *AsyncSink) Shutdown(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("async sink is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.closing)
	}
	noSend := a.noSend
	a.mu.Unlock()
	select {
	case <-noSend:
	case <-ctx.Done():
		return ctx.Err()
	}
	a.doneOnce.Do(func() { close(a.done) })
	select {
	case <-a.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *AsyncSink) enqueueDropNewest(value event.Event) error {
	if !a.beginSend() {
		return fmt.Errorf("async sink is closed")
	}
	defer a.endSend()
	a.reserve()
	select {
	case a.queue <- value:
	default:
		a.release()
		callErrorHandler(a.onError, fmt.Errorf("async sink queue full: dropped newest event %q", value.ID))
	}
	return nil
}

func (a *AsyncSink) enqueueDropOldest(value event.Event) error {
	if !a.beginSend() {
		return fmt.Errorf("async sink is closed")
	}
	defer a.endSend()
	a.reserve()
	select {
	case a.queue <- value:
		return nil
	default:
	}
	select {
	case dropped := <-a.queue:
		a.release()
		callErrorHandler(a.onError, fmt.Errorf("async sink queue full: dropped oldest event %q", dropped.ID))
	default:
	}
	select {
	case a.queue <- value:
	default:
		a.release()
		callErrorHandler(a.onError, fmt.Errorf("async sink queue full: dropped newest event %q", value.ID))
	}
	return nil
}

func (a *AsyncSink) enqueueBlocking(value event.Event) error {
	if !a.beginSend() {
		return fmt.Errorf("async sink is closed")
	}
	defer a.endSend()
	a.reserve()
	select {
	case a.queue <- value:
		return nil
	case <-a.closing:
		a.release()
		return fmt.Errorf("async sink is closed")
	}
}

func (a *AsyncSink) beginSend() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	if a.sending == 0 {
		a.noSend = make(chan struct{})
	}
	a.sending++
	return true
}

func (a *AsyncSink) endSend() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sending > 0 {
		a.sending--
	}
	if a.sending == 0 {
		close(a.noSend)
	}
}

func (a *AsyncSink) reserve() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == 0 {
		a.zero = make(chan struct{})
	}
	a.pending++
}

func (a *AsyncSink) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == 0 {
		return
	}
	a.pending--
	if a.pending == 0 {
		close(a.zero)
	}
}

func (a *AsyncSink) run() {
	defer close(a.stopped)
	for {
		select {
		case value := <-a.queue:
			if err := callSink(a.sink, value); err != nil {
				callErrorHandler(a.onError, err)
			}
			a.release()
		case <-a.done:
			for {
				select {
				case value := <-a.queue:
					if err := callSink(a.sink, value); err != nil {
						callErrorHandler(a.onError, err)
					}
					a.release()
				default:
					return
				}
			}
		}
	}
}

func closedChannel() chan struct{} {
	value := make(chan struct{})
	close(value)
	return value
}
