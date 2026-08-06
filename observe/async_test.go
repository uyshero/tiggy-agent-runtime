package observe

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestAsyncSinkFlushDeliversEvents(t *testing.T) {
	var (
		mu        sync.Mutex
		delivered []string
	)
	async, err := NewAsyncSink(func(value event.Event) error {
		mu.Lock()
		delivered = append(delivered, value.ID)
		mu.Unlock()
		return nil
	}, AsyncOptions{QueueSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.Shutdown(context.Background()) })

	value := testEvent(t, "run_async", "evt_1", event.RunStarted, 1, time.Now(), "", event.RunStartedPayload{Agent: "agent"})
	if err := async.Sink(value); err != nil {
		t.Fatalf("Sink() error = %v", err)
	}
	if err := async.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 || delivered[0] != "evt_1" {
		t.Fatalf("delivered = %#v", delivered)
	}
}

func TestAsyncBatchSinkCoalescesAvailableEvents(t *testing.T) {
	var (
		mu      sync.Mutex
		batches [][]string
		calls   int
	)
	started := make(chan struct{})
	release := make(chan struct{})
	async, err := NewAsyncBatchSink(func(values []event.Event) error {
		ids := make([]string, len(values))
		for index, value := range values {
			ids[index] = value.ID
		}
		mu.Lock()
		batches = append(batches, ids)
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(started)
			<-release
		}
		return nil
	}, AsyncBatchOptions{QueueSize: 4, BatchSize: 3, DropPolicy: AsyncBlock})
	if err != nil {
		t.Fatal(err)
	}

	events := asyncEvents(t, "batch", 5)
	if err := async.Sink(events[0]); err != nil {
		t.Fatalf("Sink(first) error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first batch")
	}
	for _, value := range events[1:] {
		if err := async.Sink(value); err != nil {
			t.Fatalf("Sink(%s) error = %v", value.ID, err)
		}
	}
	close(release)
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := [][]string{{"evt_1"}, {"evt_2", "evt_3", "evt_4"}, {"evt_5"}}
	if len(batches) != len(want) {
		t.Fatalf("batches = %#v, want %#v", batches, want)
	}
	for index := range want {
		if strings.Join(batches[index], ",") != strings.Join(want[index], ",") {
			t.Fatalf("batches = %#v, want %#v", batches, want)
		}
	}
	stats := async.Stats()
	if stats.BatchSize != 3 || stats.QueueCapacity != 4 || stats.Processed != 5 ||
		stats.DelegateCalls != 3 || stats.DelegateFailures != 0 || stats.Pending != 0 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestAsyncBatchSinkContainsDelegatePanic(t *testing.T) {
	handled := make(chan error, 1)
	async, err := NewAsyncBatchSink(func([]event.Event) error {
		panic("batch exploded")
	}, AsyncBatchOptions{
		QueueSize: 2,
		BatchSize: 2,
		OnError: func(err error) {
			handled <- err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.Shutdown(context.Background()) })

	value := testEvent(t, "batch_panic", "evt_1", event.RunStarted, 1, time.Now(), "", event.RunStartedPayload{Agent: "agent"})
	if err := async.Sink(value); err != nil {
		t.Fatalf("Sink() error = %v", err)
	}
	if err := async.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	select {
	case err := <-handled:
		if !strings.Contains(err.Error(), "observer panic: batch exploded") {
			t.Fatalf("OnError() = %v", err)
		}
	default:
		t.Fatal("OnError() was not called")
	}
	stats := async.Stats()
	if stats.Processed != 1 || stats.DelegateCalls != 1 || stats.DelegateFailures != 1 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestNewAsyncBatchSinkValidatesBatchSize(t *testing.T) {
	if _, err := NewAsyncBatchSink(nil, AsyncBatchOptions{}); err == nil {
		t.Fatal("NewAsyncBatchSink(nil) returned nil error")
	}
	if _, err := NewAsyncBatchSink(func([]event.Event) error { return nil }, AsyncBatchOptions{
		QueueSize: 1,
		BatchSize: 2,
	}); err == nil {
		t.Fatal("NewAsyncBatchSink() accepted BatchSize larger than QueueSize")
	}
	async, err := NewAsyncBatchSink(func([]event.Event) error { return nil }, AsyncBatchOptions{QueueSize: 1})
	if err != nil {
		t.Fatalf("NewAsyncBatchSink() default BatchSize error = %v", err)
	}
	if stats := async.Stats(); stats.BatchSize != 1 {
		t.Fatalf("default Stats() = %#v", stats)
	}
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAsyncSinkDropNewestReportsDroppedEvent(t *testing.T) {
	control := newBlockingSink()
	var dropped []string
	async, err := NewAsyncSink(control.sink, AsyncOptions{
		QueueSize:  1,
		DropPolicy: AsyncDropNewest,
		OnError: func(err error) {
			dropped = append(dropped, err.Error())
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := asyncEvents(t, "drop_newest", 3)
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
	if len(dropped) != 1 || !strings.Contains(dropped[0], `dropped newest event "evt_3"`) {
		t.Fatalf("dropped = %#v", dropped)
	}
	stats := async.Stats()
	if stats.QueueCapacity != 1 || stats.QueueDepth != 1 || stats.Pending != 2 ||
		stats.Submitted != 3 || stats.Processed != 0 || stats.DroppedNewest != 1 ||
		stats.DroppedOldest != 0 || stats.DelegateFailures != 0 || stats.Rejected != 0 || stats.Closed {
		t.Fatalf("Stats() while blocked = %#v", stats)
	}

	control.release()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	control.assertDelivered(t, []string{"evt_1", "evt_2"})
	stats = async.Stats()
	if stats.QueueDepth != 0 || stats.Pending != 0 || stats.Submitted != 3 ||
		stats.Processed != 2 || stats.DroppedNewest != 1 || !stats.Closed {
		t.Fatalf("Stats() after shutdown = %#v", stats)
	}
}

func TestAsyncSinkDropOldestReportsDroppedEvent(t *testing.T) {
	control := newBlockingSink()
	var dropped []string
	async, err := NewAsyncSink(control.sink, AsyncOptions{
		QueueSize:  1,
		DropPolicy: AsyncDropOldest,
		OnError: func(err error) {
			dropped = append(dropped, err.Error())
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := asyncEvents(t, "drop_oldest", 3)
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
	if len(dropped) != 1 || !strings.Contains(dropped[0], `dropped oldest event "evt_2"`) {
		t.Fatalf("dropped = %#v", dropped)
	}
	if stats := async.Stats(); stats.DroppedOldest != 1 || stats.DroppedNewest != 0 || stats.Pending != 2 {
		t.Fatalf("Stats() while blocked = %#v", stats)
	}

	control.release()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	control.assertDelivered(t, []string{"evt_1", "evt_3"})
}

func TestAsyncSinkBlockBackpressuresUntilCapacity(t *testing.T) {
	control := newBlockingSink()
	async, err := NewAsyncSink(control.sink, AsyncOptions{QueueSize: 1, DropPolicy: AsyncBlock})
	if err != nil {
		t.Fatal(err)
	}

	events := asyncEvents(t, "block", 3)
	if err := async.Sink(events[0]); err != nil {
		t.Fatalf("Sink(first) error = %v", err)
	}
	control.waitStarted(t, "evt_1")
	if err := async.Sink(events[1]); err != nil {
		t.Fatalf("Sink(second) error = %v", err)
	}

	returned := make(chan error, 1)
	go func() {
		returned <- async.Sink(events[2])
	}()
	select {
	case err := <-returned:
		t.Fatalf("third Sink returned before capacity was available: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	control.release()
	if err := <-returned; err != nil {
		t.Fatalf("third Sink error = %v", err)
	}
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	control.assertDelivered(t, []string{"evt_1", "evt_2", "evt_3"})
}

func TestAsyncSinkShutdownDrainsAndRejectsLaterEvents(t *testing.T) {
	var (
		mu        sync.Mutex
		delivered []string
	)
	async, err := NewAsyncSink(func(value event.Event) error {
		mu.Lock()
		delivered = append(delivered, value.ID)
		mu.Unlock()
		return nil
	}, AsyncOptions{QueueSize: 4})
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range asyncEvents(t, "shutdown", 3) {
		if err := async.Sink(value); err != nil {
			t.Fatalf("Sink(%s) error = %v", value.ID, err)
		}
	}
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := async.Sink(testEvent(t, "shutdown", "evt_4", event.RunCompleted, 4, time.Now(), "", event.RunTerminalPayload{Status: "completed"})); err == nil {
		t.Fatal("Sink() after Shutdown returned nil")
	}
	if stats := async.Stats(); !stats.Closed || stats.Rejected != 1 || stats.Processed != 3 || stats.Pending != 0 {
		t.Fatalf("Stats() after rejected event = %#v", stats)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Join(delivered, ",") != "evt_1,evt_2,evt_3" {
		t.Fatalf("delivered = %#v", delivered)
	}
}

func TestAsyncSinkFlushReturnsContextError(t *testing.T) {
	control := newBlockingSink()
	async, err := NewAsyncSink(control.sink, AsyncOptions{QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}

	value := testEvent(t, "flush_timeout", "evt_1", event.RunStarted, 1, time.Now(), "", event.RunStartedPayload{Agent: "agent"})
	if err := async.Sink(value); err != nil {
		t.Fatalf("Sink() error = %v", err)
	}
	control.waitStarted(t, "evt_1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := async.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Flush() error = %v, want deadline exceeded", err)
	}

	control.release()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAsyncSinkAbortDoesNotWaitAndDiscardsQueue(t *testing.T) {
	control := newBlockingSink()
	async, err := NewAsyncSink(control.sink, AsyncOptions{QueueSize: 2, DropPolicy: AsyncBlock})
	if err != nil {
		t.Fatal(err)
	}

	events := asyncEvents(t, "abort", 4)
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

	abortReturned := make(chan struct{})
	go func() {
		async.Abort()
		async.Abort()
		close(abortReturned)
	}()
	select {
	case <-abortReturned:
	case <-time.After(time.Second):
		t.Fatal("Abort() waited for blocked delegate")
	}
	if err := async.Sink(events[3]); err == nil {
		t.Fatal("Sink() after Abort returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := async.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() while delegate blocked = %v, want deadline exceeded", err)
	}

	control.release()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() after release error = %v", err)
	}
	control.assertDelivered(t, []string{"evt_1"})
	stats := async.Stats()
	if !stats.Closed || !stats.Aborted || stats.Pending != 0 || stats.Submitted != 3 ||
		stats.Processed != 1 || stats.Discarded != 2 || stats.Rejected != 1 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestAsyncSinkAbortWaitsForInFlightSenderBeforeDiscarding(t *testing.T) {
	control := newBlockingSink()
	dropReported := make(chan struct{})
	continueDrop := make(chan struct{})
	async, err := NewAsyncSink(control.sink, AsyncOptions{
		QueueSize:  1,
		DropPolicy: AsyncDropOldest,
		OnError: func(err error) {
			if strings.Contains(err.Error(), `dropped oldest event "evt_2"`) {
				close(dropReported)
				<-continueDrop
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := asyncEvents(t, "abort_in_flight", 3)
	if err := async.Sink(events[0]); err != nil {
		t.Fatalf("Sink(first) error = %v", err)
	}
	control.waitStarted(t, "evt_1")
	if err := async.Sink(events[1]); err != nil {
		t.Fatalf("Sink(second) error = %v", err)
	}
	thirdReturned := make(chan error, 1)
	go func() {
		thirdReturned <- async.Sink(events[2])
	}()
	select {
	case <-dropReported:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight drop handler")
	}

	async.Abort()
	control.release()
	close(continueDrop)
	if err := <-thirdReturned; err != nil {
		t.Fatalf("Sink(third) error = %v", err)
	}
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	control.assertDelivered(t, []string{"evt_1"})
	stats := async.Stats()
	if stats.Pending != 0 || stats.Submitted != 3 || stats.Processed != 1 ||
		stats.DroppedOldest != 1 || stats.Discarded != 1 || !stats.Aborted {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestAsyncSinkReportsDelegateErrorsAndPanics(t *testing.T) {
	var (
		mu       sync.Mutex
		handled  []string
		attempts int
	)
	async, err := NewAsyncSink(func(event.Event) error {
		attempts++
		if attempts == 1 {
			return errors.New("delegate failed")
		}
		panic("delegate exploded")
	}, AsyncOptions{
		QueueSize: 2,
		OnError: func(err error) {
			mu.Lock()
			handled = append(handled, err.Error())
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.Shutdown(context.Background()) })

	for _, value := range asyncEvents(t, "delegate", 2) {
		if err := async.Sink(value); err != nil {
			t.Fatalf("Sink(%s) error = %v", value.ID, err)
		}
	}
	if err := async.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 2 || !strings.Contains(handled[0], "delegate failed") || !strings.Contains(handled[1], "observer panic: delegate exploded") {
		t.Fatalf("handled = %#v", handled)
	}
	if stats := async.Stats(); stats.Processed != 2 || stats.DelegateFailures != 2 || stats.Pending != 0 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestAsyncSinkOwnsEventPayload(t *testing.T) {
	entered := make(chan struct{})
	inspect := make(chan struct{})
	seen := make(chan string, 1)
	async, err := NewAsyncSink(func(value event.Event) error {
		close(entered)
		<-inspect
		seen <- string(value.Payload)
		return nil
	}, AsyncOptions{QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.Shutdown(context.Background()) })

	value := testEvent(t, "ownership", "evt_1", event.RunStarted, 1, time.Now(), "", event.RunStartedPayload{Agent: "agent"})
	if err := async.Sink(value); err != nil {
		t.Fatalf("Sink() error = %v", err)
	}
	<-entered
	for index := range value.Payload {
		value.Payload[index] = 'x'
	}
	close(inspect)
	if payload := <-seen; !strings.Contains(payload, `"agent":"agent"`) {
		t.Fatalf("payload = %q", payload)
	}
}

func TestAsyncSinkStatsConcurrentAccounting(t *testing.T) {
	async, err := NewAsyncSink(func(event.Event) error {
		return nil
	}, AsyncOptions{QueueSize: 16, DropPolicy: AsyncDropNewest})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = async.Shutdown(context.Background()) })

	value := testEvent(t, "stats_concurrent", "evt_shared", event.ModelCallDelta, 1, time.Now(), "", event.ModelDeltaPayload{
		CallID: "call_1",
		Delta:  model.Delta{Type: model.DeltaText, Text: "delta"},
	})
	const (
		producers   = 8
		perProducer = 100
	)
	var producersWG sync.WaitGroup
	producersWG.Add(producers)
	for range producers {
		go func() {
			defer producersWG.Done()
			for range perProducer {
				if err := async.Sink(value); err != nil {
					t.Errorf("Sink() error = %v", err)
					return
				}
			}
		}()
	}
	producersDone := make(chan struct{})
	go func() {
		producersWG.Wait()
		close(producersDone)
	}()
	for {
		select {
		case <-producersDone:
			goto produced
		default:
			_ = async.Stats()
		}
	}

produced:
	if err := async.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	stats := async.Stats()
	if stats.Submitted != producers*perProducer || stats.Pending != 0 ||
		stats.Processed+stats.DroppedNewest+stats.DroppedOldest+stats.Discarded != stats.Submitted ||
		stats.DelegateFailures != 0 || stats.Rejected != 0 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestAsyncSinkStressAccounting(t *testing.T) {
	const (
		producers   = 16
		perProducer = 250
		queueSize   = 32
	)
	async, err := NewAsyncBatchSink(func([]event.Event) error {
		return nil
	}, AsyncBatchOptions{
		QueueSize:  queueSize,
		BatchSize:  8,
		DropPolicy: AsyncDropNewest,
	})
	if err != nil {
		t.Fatal(err)
	}

	value := testEvent(t, "stress", "evt_shared", event.ModelCallDelta, 1, time.Now(), "", event.ModelDeltaPayload{
		CallID: "call_1",
		Delta:  model.Delta{Type: model.DeltaText, Text: "delta"},
	})
	var group sync.WaitGroup
	group.Add(producers)
	for range producers {
		go func() {
			defer group.Done()
			for range perProducer {
				if err := async.Sink(value); err != nil {
					t.Errorf("Sink() error = %v", err)
					return
				}
			}
		}()
	}
	group.Wait()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	stats := async.Stats()
	if stats.Submitted != producers*perProducer || stats.Pending != 0 || stats.QueueDepth != 0 ||
		stats.Processed+stats.DroppedNewest+stats.DroppedOldest+stats.Discarded != stats.Submitted ||
		stats.DelegateCalls > stats.Processed || stats.BatchSize != 8 || stats.QueueCapacity != queueSize {
		t.Fatalf("Stats() = %#v", stats)
	}
}

type blockingSink struct {
	started   chan string
	releaseCh chan struct{}

	mu        sync.Mutex
	delivered []string
}

func newBlockingSink() *blockingSink {
	return &blockingSink{
		started:   make(chan string, 8),
		releaseCh: make(chan struct{}),
	}
}

func (b *blockingSink) sink(value event.Event) error {
	b.started <- value.ID
	<-b.releaseCh
	b.mu.Lock()
	b.delivered = append(b.delivered, value.ID)
	b.mu.Unlock()
	return nil
}

func (b *blockingSink) waitStarted(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-b.started:
		if got != want {
			t.Fatalf("started = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s to start", want)
	}
}

func (b *blockingSink) release() {
	close(b.releaseCh)
}

func (b *blockingSink) assertDelivered(t *testing.T, want []string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.Join(b.delivered, ",") != strings.Join(want, ",") {
		t.Fatalf("delivered = %#v, want %#v", b.delivered, want)
	}
}

func asyncEvents(t *testing.T, runID string, count int) []event.Event {
	t.Helper()
	values := make([]event.Event, 0, count)
	now := time.Now()
	for index := 1; index <= count; index++ {
		values = append(values, testEvent(t, runID, "evt_"+strconv.Itoa(index), event.ModelCallDelta, int64(index), now.Add(time.Duration(index)*time.Millisecond), "", event.ModelDeltaPayload{
			CallID: "call_1",
			Delta:  model.Delta{Type: model.DeltaText, Text: "delta"},
		}))
	}
	return values
}
