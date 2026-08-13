package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestInMemoryStoreRoundTripOwnershipAndIdempotency(t *testing.T) {
	store, err := NewInMemoryStore(InMemoryStoreOptions{MaxObjects: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	original := NewArtifact("result.json", "application/json", []byte(`{"ok":true}`))
	request := PutRequest{ExecutionID: "execution-1", TaskID: "one", IdempotencyKey: "key-1", Artifact: original}
	reference, err := store.Put(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	original.Data[0] = 'x'
	loaded, err := store.Get(context.Background(), reference)
	if err != nil || string(loaded.Data) != `{"ok":true}` {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	loaded.Data[0] = 'x'
	again, err := store.Get(context.Background(), reference)
	if err != nil || string(again.Data) != `{"ok":true}` {
		t.Fatalf("owned load = %#v, err = %v", again, err)
	}
	request.Artifact = NewArtifact("result.json", "application/json", []byte(`{"ok":true}`))
	duplicate, err := store.Put(context.Background(), request)
	if err != nil || duplicate.ObjectRefID != reference.ObjectRefID {
		t.Fatalf("duplicate = %#v, err = %v", duplicate, err)
	}
	request.IdempotencyKey = "key-2"
	if _, err := store.Put(context.Background(), request); err != nil {
		t.Fatalf("content-addressed reuse at capacity failed: %v", err)
	}
	request.IdempotencyKey = "key-1"
	request.Artifact = NewArtifact("result.json", "application/json", []byte(`{"ok":false}`))
	if _, err := store.Put(context.Background(), request); err == nil {
		t.Fatal("expected idempotency conflict")
	}
}

func TestInMemoryStoreBoundsAndConcurrency(t *testing.T) {
	store, err := NewInMemoryStore(InMemoryStoreOptions{MaxObjects: 32, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			artifact := NewArtifact(fmt.Sprintf("%d.txt", index), "text/plain", []byte(fmt.Sprintf("value-%d", index)))
			request := PutRequest{ExecutionID: "execution-1", TaskID: fmt.Sprintf("task-%d", index), IdempotencyKey: fmt.Sprintf("key-%d", index), Artifact: artifact}
			reference, err := store.Put(context.Background(), request)
			if err == nil {
				_, err = store.Get(context.Background(), reference)
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	overflow := PutRequest{ExecutionID: "execution-1", TaskID: "overflow", IdempotencyKey: "overflow", Artifact: NewArtifact("overflow.txt", "text/plain", []byte("overflow"))}
	if _, err := store.Put(context.Background(), overflow); err == nil {
		t.Fatal("expected object capacity error")
	}
}

func TestOrchestratorStoresReferencesAndReloadsInputs(t *testing.T) {
	store, err := NewInMemoryStore(InMemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker", "one")}}
	o := Orchestrator{Store: store, Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, inputs []Artifact) (Artifact, error) {
		if task.ID == "two" && (len(inputs) != 1 || string(inputs[0].Data) != `"one"`) {
			return Artifact{}, fmt.Errorf("input = %#v", inputs)
		}
		return output(task, task.ID), nil
	}}}}
	report, err := o.Run(context.Background(), brief)
	if err != nil || report.Status != StatusCompleted {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
	if len(report.Artifacts) != 0 || len(report.ArtifactReferences) != 2 || report.Tasks[0].Artifact != nil || report.Tasks[0].ArtifactReference == nil {
		t.Fatalf("report did not externalize artifacts: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), `"data"`) || strings.Contains(string(encoded), `\"one\"`) {
		t.Fatalf("Report contains Artifact body: %s, err = %v", encoded, err)
	}
	loaded, err := store.Get(context.Background(), report.ArtifactReferences[1])
	if err != nil || string(loaded.Data) != `"two"` {
		t.Fatalf("stored output = %#v, err = %v", loaded, err)
	}
}

func TestOrchestratorRejectsStoreFailures(t *testing.T) {
	task := validTask("one", "worker")
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}}
	for name, store := range map[string]Store{
		"error": storeDouble{
			put: func(context.Context, PutRequest) (ArtifactReference, error) {
				return ArtifactReference{}, errors.New("offline")
			},
			get: func(context.Context, ArtifactReference) (Artifact, error) { return Artifact{}, nil },
		},
		"panic": storeDouble{
			put: func(context.Context, PutRequest) (ArtifactReference, error) { panic("boom") },
			get: func(context.Context, ArtifactReference) (Artifact, error) { return Artifact{}, nil },
		},
		"invalid reference": storeDouble{
			put: func(_ context.Context, request PutRequest) (ArtifactReference, error) {
				return ReferenceFromArtifact("https://invalid.example/object", request.Artifact), nil
			},
			get: func(context.Context, ArtifactReference) (Artifact, error) { return Artifact{}, nil },
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := Orchestrator{Store: store, Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}}}
			report, err := o.Run(context.Background(), brief)
			var runErr *RunError
			if !errors.As(err, &runErr) || runErr.Code != "artifact_store_failed" || report.Tasks[0].Status != TaskFailed {
				t.Fatalf("report = %#v, run error = %#v, err = %v", report, runErr, err)
			}
		})
	}
}

type storeDouble struct {
	put func(context.Context, PutRequest) (ArtifactReference, error)
	get func(context.Context, ArtifactReference) (Artifact, error)
}

func (s storeDouble) Put(ctx context.Context, request PutRequest) (ArtifactReference, error) {
	return s.put(ctx, request.Clone())
}

func (s storeDouble) Get(ctx context.Context, reference ArtifactReference) (Artifact, error) {
	return s.get(ctx, reference.Clone())
}

func TestOrchestratorRejectsTamperedStoredInput(t *testing.T) {
	base, err := NewInMemoryStore(InMemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := storeDouble{
		put: base.Put,
		get: func(ctx context.Context, reference ArtifactReference) (Artifact, error) {
			value, err := base.Get(ctx, reference)
			if err == nil {
				value.Data = []byte(`"tampered"`)
			}
			return value, err
		},
	}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker", "one")}}
	o := Orchestrator{Store: store, Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}}}
	report, err := o.Run(context.Background(), brief)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != "artifact_load_failed" || report.Tasks[1].Status != TaskFailed {
		t.Fatalf("report = %#v, run error = %#v, err = %v", report, runErr, err)
	}
}

func TestOrchestratorArtifactBudgets(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker")}}
	o := Orchestrator{
		ArtifactLimits: ArtifactLimits{MaxArtifactBytes: 5, MaxExecutionArtifactBytes: 9},
		Subagents:      Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}},
	}
	report, err := o.Run(context.Background(), brief)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != "artifact_limit_exceeded" || report.Tasks[0].Status != TaskCompleted || report.Tasks[1].Status != TaskFailed {
		t.Fatalf("report = %#v, run error = %#v, err = %v", report, runErr, err)
	}
}
