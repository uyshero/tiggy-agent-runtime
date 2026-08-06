package observe

import (
	"context"
	"fmt"
	"runtime/debug"
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

// BatchSink consumes one owned non-empty Event batch.
// It must return quickly or provide its own bounded I/O.
type BatchSink func([]event.Event) error

// AsyncBatchOptions configures an AsyncSink backed by a BatchSink.
// QueueSize defaults to 1024 and BatchSize defaults to 64.
// Batching drains only Events already available in the queue and adds no timer delay.
type AsyncBatchOptions struct {
	QueueSize  int
	BatchSize  int
	DropPolicy AsyncDropPolicy
	OnError    ErrorHandler
}

// AsyncStats is a content-free point-in-time snapshot of one AsyncSink.
// QueueDepth counts buffered Events only; Pending also includes an Event or batch currently being
// processed and an AsyncBlock submission waiting for queue capacity.
type AsyncStats struct {
	QueueCapacity    int
	BatchSize        int
	QueueDepth       int
	Pending          int
	Submitted        uint64
	Processed        uint64
	DelegateCalls    uint64
	DroppedNewest    uint64
	DroppedOldest    uint64
	Discarded        uint64
	DelegateFailures uint64
	Rejected         uint64
	Closed           bool
	Aborted          bool
}

// AsyncSink wraps a synchronous Sink with a bounded single-worker queue.
//
// It is intended for operational logging and telemetry that should not run inline with Agent execution.
// Strict audit and required recording should normally use a direct synchronous Sink instead.
type AsyncSink struct {
	sink       event.Sink
	batchSink  BatchSink
	batchSize  int
	queue      chan event.Event
	dropPolicy AsyncDropPolicy
	onError    ErrorHandler

	mu               sync.Mutex
	closed           bool
	pending          int
	sending          int
	zero             chan struct{}
	noSend           chan struct{}
	submitted        uint64
	processed        uint64
	delegateCalls    uint64
	droppedNewest    uint64
	droppedOldest    uint64
	discarded        uint64
	delegateFailures uint64
	rejected         uint64
	closing          chan struct{}
	done             chan struct{}
	doneOnce         sync.Once
	abort            chan struct{}
	abortOnce        sync.Once
	stopped          chan struct{}
}

// NewAsyncSink starts an AsyncSink worker around sink.
func NewAsyncSink(sink event.Sink, options AsyncOptions) (*AsyncSink, error) {
	if sink == nil {
		return nil, fmt.Errorf("async sink delegate is required")
	}
	return newAsyncSink(sink, nil, options.QueueSize, 1, options.DropPolicy, options.OnError)
}

// NewAsyncBatchSink starts an AsyncSink worker that coalesces immediately available Events.
func NewAsyncBatchSink(sink BatchSink, options AsyncBatchOptions) (*AsyncSink, error) {
	if sink == nil {
		return nil, fmt.Errorf("async batch sink delegate is required")
	}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = 1024
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = 64
		if batchSize > queueSize {
			batchSize = queueSize
		}
	}
	if batchSize > queueSize {
		return nil, fmt.Errorf("async batch size %d exceeds queue size %d", batchSize, queueSize)
	}
	return newAsyncSink(nil, sink, queueSize, batchSize, options.DropPolicy, options.OnError)
}

func newAsyncSink(sink event.Sink, batchSink BatchSink, queueSize, batchSize int, dropPolicy AsyncDropPolicy, onError ErrorHandler) (*AsyncSink, error) {
	if queueSize <= 0 {
		queueSize = 1024
	}
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
		batchSink:  batchSink,
		batchSize:  batchSize,
		queue:      make(chan event.Event, queueSize),
		dropPolicy: dropPolicy,
		onError:    onError,
		zero:       closedChannel(),
		noSend:     closedChannel(),
		closing:    make(chan struct{}),
		done:       make(chan struct{}),
		abort:      make(chan struct{}),
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

// Stats returns a content-free snapshot without changing delivery behavior.
func (a *AsyncSink) Stats() AsyncStats {
	if a == nil {
		return AsyncStats{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return AsyncStats{
		QueueCapacity:    cap(a.queue),
		BatchSize:        a.batchSize,
		QueueDepth:       len(a.queue),
		Pending:          a.pending,
		Submitted:        a.submitted,
		Processed:        a.processed,
		DelegateCalls:    a.delegateCalls,
		DroppedNewest:    a.droppedNewest,
		DroppedOldest:    a.droppedOldest,
		Discarded:        a.discarded,
		DelegateFailures: a.delegateFailures,
		Rejected:         a.rejected,
		Closed:           a.closed,
		Aborted:          a.aborted(),
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

// Abort stops accepting new Events and asks the worker to discard queued Events without waiting.
// It cannot preempt a delegate Sink invocation that is already running. Call Shutdown later when
// the application wants to wait for that invocation and the worker goroutine to finish.
func (a *AsyncSink) Abort() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.closing)
	}
	a.mu.Unlock()
	a.abortOnce.Do(func() { close(a.abort) })
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
		a.dropNewest()
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
		a.dropOldest()
		callErrorHandler(a.onError, fmt.Errorf("async sink queue full: dropped oldest event %q", dropped.ID))
	default:
	}
	select {
	case a.queue <- value:
	default:
		a.dropNewest()
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
		a.rejectPending()
		return fmt.Errorf("async sink is closed")
	}
}

func (a *AsyncSink) beginSend() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		a.rejected++
		return false
	}
	a.submitted++
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

func (a *AsyncSink) complete(count int, failed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.processed += uint64(count)
	a.delegateCalls++
	if failed {
		a.delegateFailures++
	}
	a.releasePendingLocked(count)
}

func (a *AsyncSink) dropNewest() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.droppedNewest++
	a.releasePendingLocked(1)
}

func (a *AsyncSink) dropOldest() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.droppedOldest++
	a.releasePendingLocked(1)
}

func (a *AsyncSink) rejectPending() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rejected++
	a.releasePendingLocked(1)
}

func (a *AsyncSink) discardPending(count int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discarded += uint64(count)
	a.releasePendingLocked(count)
}

func (a *AsyncSink) releasePendingLocked(count int) {
	if a.pending == 0 || count <= 0 {
		return
	}
	if count > a.pending {
		count = a.pending
	}
	a.pending -= count
	if a.pending == 0 {
		close(a.zero)
	}
}

func (a *AsyncSink) run() {
	defer close(a.stopped)
	for {
		if a.abortRequested() {
			a.finishAbort()
			return
		}
		select {
		case <-a.abort:
			a.finishAbort()
			return
		case value := <-a.queue:
			if !a.process(value) {
				return
			}
		case <-a.done:
			for {
				if a.abortRequested() {
					a.finishAbort()
					return
				}
				select {
				case <-a.abort:
					a.finishAbort()
					return
				case value := <-a.queue:
					if !a.process(value) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (a *AsyncSink) process(first event.Event) bool {
	batch := a.collectBatch(first)
	if a.abortRequested() {
		a.discardPending(len(batch))
		a.finishAbort()
		return false
	}
	var err error
	if a.batchSink != nil {
		err = callBatchSink(a.batchSink, batch)
	} else {
		err = callSink(a.sink, batch[0])
	}
	a.complete(len(batch), err != nil)
	if err != nil {
		callErrorHandler(a.onError, err)
	}
	return true
}

func (a *AsyncSink) collectBatch(first event.Event) []event.Event {
	batch := make([]event.Event, 1, a.batchSize)
	batch[0] = first
	for len(batch) < a.batchSize {
		select {
		case value := <-a.queue:
			batch = append(batch, value)
		default:
			return batch
		}
	}
	return batch
}

func (a *AsyncSink) finishAbort() {
	a.mu.Lock()
	noSend := a.noSend
	a.mu.Unlock()
	<-noSend
	a.discardQueued()
}

func (a *AsyncSink) abortRequested() bool {
	select {
	case <-a.abort:
		return true
	default:
		return false
	}
}

func (a *AsyncSink) aborted() bool {
	return a.abortRequested()
}

func (a *AsyncSink) discardQueued() {
	count := 0
	for {
		select {
		case <-a.queue:
			count++
		default:
			a.discardPending(count)
			return
		}
	}
}

func callBatchSink(sink BatchSink, values []event.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("observer panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return sink(values)
}

func closedChannel() chan struct{} {
	value := make(chan struct{})
	close(value)
	return value
}
