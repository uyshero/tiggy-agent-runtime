package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/memory"
	"github.com/uyshero/tiggy-agent-runtime/message"
)

func TestNoopIsEmptyAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	port := memory.Noop{}
	result, err := port.Recall(context.Background(), memory.RecallRequest{})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("Recall() = %#v, %v", result, err)
	}
	if err := port.Observe(context.Background(), memory.ObserveRequest{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := port.Recall(ctx, memory.RecallRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recall cancellation error = %v", err)
	}
	if err := port.Observe(ctx, memory.ObserveRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Observe cancellation error = %v", err)
	}
}

func TestNewInMemoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	scope := memory.Scope{Namespace: "workspace", Key: "account"}
	for _, options := range []memory.InMemoryOptions{
		{MaxStoredItems: -1},
		{MaxStoredBytes: 1},
		{ConflictPolicy: "merge"},
		{InitialItems: []memory.Item{{ID: "invalid"}}},
		{InitialItems: []memory.Item{memoryItem("same", scope, "one"), memoryItem("same", scope, "two")}},
		{InitialItems: []memory.Item{memoryItem("one", scope, "one"), memoryItem("two", scope, "two")}, MaxStoredItems: 1},
	} {
		_, err := memory.NewInMemory(options)
		assertMemoryCode(t, err, memory.ErrorInvalidConfig)
	}
}

func TestInMemoryExpiryIsInclusiveAndReleasesCapacity(t *testing.T) {
	t.Parallel()

	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Nanosecond)
	future := now.Add(time.Hour)
	scope := memory.Scope{Namespace: "workspace", Key: "expiry"}
	request := observeRequest("run_after_expiry", scope)
	port, err := memory.NewInMemory(memory.InMemoryOptions{
		Now: func() time.Time { return now }, MaxStoredItems: 2,
		InitialItems: []memory.Item{
			memoryItemWithExpiry("expired", scope, "old", &past),
			memoryItemWithExpiry("active", scope, "current", &future),
		},
		Admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
			return []memory.Item{observedMemoryItem("replacement", request, request.Scope, "new")}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := port.Recall(context.Background(), recallRequest("recall_expiry", scope, 3))
	if err != nil || len(before.Items) != 1 || before.Items[0].ID != "active" {
		t.Fatalf("expired Item was recalled: %#v, %v", before, err)
	}
	if err := port.Observe(context.Background(), request); err != nil {
		t.Fatalf("expired Item did not release capacity: %v", err)
	}
	after, err := port.Recall(context.Background(), recallRequest("recall_after_expiry", scope, 3))
	if err != nil || len(after.Items) != 2 || after.Items[0].ID != "replacement" {
		t.Fatalf("items after expiry cleanup = %#v, %v", after, err)
	}

	atBoundary := now
	rejected, err := memory.NewInMemory(memory.InMemoryOptions{
		Now: func() time.Time { return now },
		Admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
			item := observedMemoryItem("already-expired", request, request.Scope, "old")
			item.ExpiresAt = &atBoundary
			return []memory.Item{item}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryCode(t, rejected.Observe(context.Background(), observeRequest("run_boundary", scope)), memory.ErrorInvalidResult)
}

func TestInMemoryClockPanicIsContained(t *testing.T) {
	t.Parallel()

	clock := func() time.Time { panic("clock boom") }
	recallPort, err := memory.NewInMemory(memory.InMemoryOptions{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryCode(t, func() error {
		_, recallErr := recallPort.Recall(context.Background(), recallRequest(
			"recall_clock_panic",
			memory.Scope{Namespace: "workspace", Key: "clock"},
			1,
		))
		return recallErr
	}(), memory.ErrorPanic)

	observePort, err := memory.NewInMemory(memory.InMemoryOptions{
		Now: clock,
		Admission: memory.AdmissionFunc(func(context.Context, memory.ObserveRequest) ([]memory.Item, error) {
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryCode(t, observePort.Observe(
		context.Background(),
		observeRequest("observe_clock_panic", memory.Scope{Namespace: "workspace", Key: "clock"}),
	), memory.ErrorPanic)
}

func TestInMemoryConflictPolicies(t *testing.T) {
	t.Parallel()

	scope := memory.Scope{Namespace: "workspace", Key: "conflict"}
	for _, test := range []struct {
		name      string
		policy    memory.ConflictPolicy
		wantError memory.ErrorCode
		wantText  string
	}{
		{name: "reject by default", policy: "", wantError: memory.ErrorConflict, wantText: "old"},
		{name: "keep existing", policy: memory.ConflictKeepExisting, wantText: "old"},
		{name: "replace existing", policy: memory.ConflictReplaceExisting, wantText: "new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			port, err := memory.NewInMemory(memory.InMemoryOptions{
				ConflictPolicy: test.policy,
				InitialItems:   []memory.Item{memoryItem("same", scope, "old")},
				Admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
					return []memory.Item{observedMemoryItem("same", request, request.Scope, "new")}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			err = port.Observe(context.Background(), observeRequest("run_conflict", scope))
			if test.wantError != "" {
				assertMemoryCode(t, err, test.wantError)
			} else if err != nil {
				t.Fatal(err)
			}
			result, recallErr := port.Recall(context.Background(), recallRequest("recall_conflict", scope, 1))
			if recallErr != nil || len(result.Items) != 1 || result.Items[0].Text != test.wantText {
				t.Fatalf("conflict result = %#v, %v", result, recallErr)
			}
		})
	}
}

func TestInMemoryObservedProvenanceMustReferenceCurrentTurn(t *testing.T) {
	t.Parallel()

	scope := memory.Scope{Namespace: "workspace", Key: "provenance"}
	for _, provenance := range []memory.Provenance{
		{Source: "test", RunID: "another-run", MessageIDs: []string{"assistant_run_provenance"}},
		{Source: "test", RunID: "run_provenance"},
		{Source: "test", RunID: "run_provenance", MessageIDs: []string{"outside-message"}},
	} {
		provenance := provenance
		port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: memory.AdmissionFunc(
			func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
				return []memory.Item{{ID: "invalid", Scope: request.Scope, Text: "bad", Provenance: provenance}}, nil
			},
		)})
		if err != nil {
			t.Fatal(err)
		}
		assertMemoryCode(t, port.Observe(context.Background(), observeRequest("run_provenance", scope)), memory.ErrorInvalidResult)
	}
}

func TestInMemoryRecallIsScopedRecentBoundedAndOwned(t *testing.T) {
	t.Parallel()

	first := memory.Scope{Namespace: "workspace", Key: "first"}
	second := memory.Scope{Namespace: "workspace", Key: "second"}
	items := []memory.Item{
		memoryItem("one", first, "old"),
		memoryItem("foreign", second, "other scope"),
		memoryItem("two", first, "middle"),
		memoryItem("three", first, "new"),
	}
	port, err := memory.NewInMemory(memory.InMemoryOptions{InitialItems: items})
	if err != nil {
		t.Fatal(err)
	}
	items[0].Text = "mutated after construction"

	result, err := port.Recall(context.Background(), recallRequest("recall_1", first, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ID != "three" || result.Items[1].ID != "two" {
		t.Fatalf("items = %#v", result.Items)
	}
	result.Items[0].Text = "mutated result"
	again, err := port.Recall(context.Background(), recallRequest("recall_2", first, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 3 || again.Items[0].Text != "new" || again.Items[2].Text != "old" {
		t.Fatalf("owned items = %#v", again.Items)
	}
	foreign, err := port.Recall(context.Background(), recallRequest("recall_3", second, 3))
	if err != nil || len(foreign.Items) != 1 || foreign.Items[0].ID != "foreign" {
		t.Fatalf("foreign scope = %#v, %v", foreign, err)
	}
}

func TestInMemoryObserveRequiresExplicitAdmission(t *testing.T) {
	t.Parallel()

	scope := memory.Scope{Namespace: "workspace", Key: "account"}
	withoutAdmission, err := memory.NewInMemory(memory.InMemoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutAdmission.Observe(context.Background(), observeRequest("run_noop", scope)); err != nil {
		t.Fatal(err)
	}
	empty, err := withoutAdmission.Recall(context.Background(), recallRequest("recall_noop", scope, 1))
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("implicit admission occurred: %#v, %v", empty, err)
	}

	port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: memory.AdmissionFunc(
		func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
			request.Messages[0].Content[0].Text = "adapter mutation"
			return []memory.Item{observedMemoryItem("admitted", request, request.Scope, "explicit evidence")}, nil
		},
	)})
	if err != nil {
		t.Fatal(err)
	}
	request := observeRequest("run_admit", scope)
	if err := port.Observe(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content[0].Text != "question" {
		t.Fatal("Admission mutated its caller's ObserveRequest")
	}
	result, err := port.Recall(context.Background(), recallRequest("recall_admit", scope, 1))
	if err != nil || len(result.Items) != 1 || result.Items[0].Text != "explicit evidence" {
		t.Fatalf("admitted result = %#v, %v", result, err)
	}
}

func TestInMemoryObserveRejectsBatchAtomically(t *testing.T) {
	t.Parallel()

	scope := memory.Scope{Namespace: "workspace", Key: "account"}
	for _, test := range []struct {
		name      string
		admission memory.Admission
		code      memory.ErrorCode
		maxItems  int
	}{
		{
			name: "conflict",
			admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
				return []memory.Item{
					observedMemoryItem("new", request, scope, "must not commit"),
					observedMemoryItem("existing", request, scope, "conflict"),
				}, nil
			}),
			code: memory.ErrorConflict, maxItems: 3,
		},
		{
			name: "capacity",
			admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
				return []memory.Item{
					observedMemoryItem("new-1", request, scope, "one"),
					observedMemoryItem("new-2", request, scope, "two"),
				}, nil
			}),
			code: memory.ErrorLimit, maxItems: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			port, err := memory.NewInMemory(memory.InMemoryOptions{
				InitialItems: []memory.Item{memoryItem("existing", scope, "original")},
				Admission:    test.admission, MaxStoredItems: test.maxItems,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = port.Observe(context.Background(), observeRequest("run_"+test.name, scope))
			assertMemoryCode(t, err, test.code)
			result, recallErr := port.Recall(context.Background(), recallRequest("recall_"+test.name, scope, 2))
			if recallErr != nil || len(result.Items) != 1 || result.Items[0].ID != "existing" {
				t.Fatalf("partial batch committed: %#v, %v", result, recallErr)
			}
		})
	}
}

func TestInMemoryAdmissionFailurePanicScopeAndCancellation(t *testing.T) {
	t.Parallel()

	scope := memory.Scope{Namespace: "workspace", Key: "account"}
	tests := []struct {
		name      string
		admission memory.Admission
		wantCode  memory.ErrorCode
	}{
		{
			name: "error",
			admission: memory.AdmissionFunc(func(context.Context, memory.ObserveRequest) ([]memory.Item, error) {
				return nil, errors.New("extractor unavailable")
			}),
			wantCode: memory.ErrorAdmission,
		},
		{
			name: "panic",
			admission: memory.AdmissionFunc(func(context.Context, memory.ObserveRequest) ([]memory.Item, error) {
				panic("admission boom")
			}),
			wantCode: memory.ErrorPanic,
		},
		{
			name: "cross scope",
			admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
				return []memory.Item{observedMemoryItem("crossed", request, memory.Scope{Namespace: "workspace", Key: "other"}, "bad")}, nil
			}),
			wantCode: memory.ErrorInvalidResult,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: test.admission})
			if err != nil {
				t.Fatal(err)
			}
			assertMemoryCode(t, port.Observe(context.Background(), observeRequest("run_"+test.name, scope)), test.wantCode)
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	port, err := memory.NewInMemory(memory.InMemoryOptions{Admission: memory.AdmissionFunc(
		func(callCtx context.Context, _ memory.ObserveRequest) ([]memory.Item, error) {
			cancel()
			<-callCtx.Done()
			return nil, callCtx.Err()
		},
	)})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.Observe(ctx, observeRequest("run_cancel", scope)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestInMemoryConcurrentObserveAndRecall(t *testing.T) {
	t.Parallel()

	const count = 64
	scope := memory.Scope{Namespace: "workspace", Key: "concurrent"}
	options := memory.DefaultOptions()
	options.RecallLimit = count
	port, err := memory.NewInMemory(memory.InMemoryOptions{
		MemoryOptions: options, MaxStoredItems: count,
		Admission: memory.AdmissionFunc(func(_ context.Context, request memory.ObserveRequest) ([]memory.Item, error) {
			return []memory.Item{observedMemoryItem("item_"+request.RunID, request, request.Scope, request.RunID)}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			runID := fmt.Sprintf("run_%03d", index)
			if err := port.Observe(context.Background(), observeRequest(runID, scope)); err != nil {
				t.Errorf("Observe(%s): %v", runID, err)
			}
			if _, err := port.Recall(context.Background(), recallRequest("recall_"+runID, scope, count)); err != nil {
				t.Errorf("Recall(%s): %v", runID, err)
			}
		}(index)
	}
	group.Wait()
	result, err := port.Recall(context.Background(), recallRequest("recall_final", scope, count))
	if err != nil || len(result.Items) != count {
		t.Fatalf("final recall count = %d, error = %v", len(result.Items), err)
	}
}

func memoryItem(id string, scope memory.Scope, text string) memory.Item {
	return memory.Item{ID: id, Scope: scope, Text: text, Provenance: memory.Provenance{Source: "test"}}
}

func memoryItemWithExpiry(id string, scope memory.Scope, text string, expiresAt *time.Time) memory.Item {
	item := memoryItem(id, scope, text)
	item.ExpiresAt = expiresAt
	return item
}

func observedMemoryItem(id string, request memory.ObserveRequest, scope memory.Scope, text string) memory.Item {
	return memory.Item{
		ID: id, Scope: scope, Text: text,
		Provenance: memory.Provenance{
			Source: "test", RunID: request.RunID,
			MessageIDs: []string{request.Messages[len(request.Messages)-1].ID},
		},
	}
}

func recallRequest(runID string, scope memory.Scope, limit int) memory.RecallRequest {
	return memory.RecallRequest{
		RunID: runID, Scope: scope, Limit: limit,
		Input: message.Message{ID: "input_" + runID, Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "question"}}},
	}
}

func observeRequest(runID string, scope memory.Scope) memory.ObserveRequest {
	return memory.ObserveRequest{RunID: runID, Scope: scope, Messages: []message.Message{
		{ID: "user_" + runID, Role: message.RoleUser, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "question"}}},
		{ID: "assistant_" + runID, Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: "answer"}}},
	}}
}

func assertMemoryCode(t *testing.T, err error, code memory.ErrorCode) {
	t.Helper()
	var memoryError *memory.Error
	if !errors.As(err, &memoryError) || memoryError.Code != code {
		t.Fatalf("error = %v, want memory code %q", err, code)
	}
}
