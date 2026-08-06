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

	control.release()
	if err := async.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	control.assertDelivered(t, []string{"evt_1", "evt_2"})
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
