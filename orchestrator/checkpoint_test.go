package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestInMemoryCheckpointStoreCASAndOwnership(t *testing.T) {
	store, err := NewInMemoryCheckpointStore(InMemoryCheckpointOptions{MaxExecutions: 1})
	if err != nil {
		t.Fatal(err)
	}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	initial, err := initialSnapshot("execution-1", brief)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.CompareAndSwap(context.Background(), "execution-1", 0, initial)
	if err != nil || saved.Version != 1 {
		t.Fatalf("saved = %#v, err = %v", saved, err)
	}
	saved.BriefID = "mutated"
	loaded, exists, err := store.Load(context.Background(), "execution-1")
	if err != nil || !exists || loaded.BriefID != "brief-1" {
		t.Fatalf("loaded = %#v, exists = %v, err = %v", loaded, exists, err)
	}
	_, err = store.CompareAndSwap(context.Background(), "execution-1", 0, initial)
	var conflict *CheckpointConflictError
	if !errors.As(err, &conflict) || conflict.Actual != 1 {
		t.Fatalf("conflict = %#v, err = %v", conflict, err)
	}
}

func TestExecutionSnapshotCloneOwnsCompletedMetadata(t *testing.T) {
	original := ExecutionSnapshot{Completed: []CheckpointTask{{
		TaskID: "one",
		Artifact: ArtifactReference{
			ObjectRefID: "object-1",
			Metadata:    []byte(`{"source":"original"}`),
		},
	}}}
	cloned := original.Clone()
	cloned.Completed[0].TaskID = "changed"
	cloned.Completed[0].Artifact.Metadata[0] = '['
	if original.Completed[0].TaskID != "one" || string(original.Completed[0].Artifact.Metadata) != `{"source":"original"}` {
		t.Fatalf("original was mutated: %#v", original)
	}
}

func TestInMemoryCheckpointStoreAllowsOneConcurrentCAS(t *testing.T) {
	store, _ := NewInMemoryCheckpointStore(InMemoryCheckpointOptions{})
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	initial, _ := initialSnapshot("execution-1", brief)
	if _, err := store.CompareAndSwap(context.Background(), "execution-1", 0, initial); err != nil {
		t.Fatal(err)
	}
	next := initial.Clone()
	next.Version = 2
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.CompareAndSwap(context.Background(), "execution-1", 1, next)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		var conflict *CheckpointConflictError
		if errors.As(err, &conflict) {
			conflicted++
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded/conflicted = %d/%d", succeeded, conflicted)
	}
}

func TestResumeContinuesAfterLastCheckpoint(t *testing.T) {
	artifacts, _ := NewInMemoryStore(InMemoryStoreOptions{})
	checkpoints, _ := NewInMemoryCheckpointStore(InMemoryCheckpointOptions{})
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker", "one")}}
	firstOrder := []string{}
	first := Orchestrator{
		Store: artifacts, Checkpoints: checkpoints, IDs: func(prefix string) string { return prefix + "-1" },
		Subagents: Subagents{"worker": scriptedSubagent{order: &firstOrder, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
			if task.ID == "two" {
				return Artifact{}, errors.New("temporary failure")
			}
			return output(task, task.ID), nil
		}}},
	}
	failed, err := first.Run(context.Background(), brief)
	if err == nil || failed.CheckpointVersion != 2 || !reflect.DeepEqual(firstOrder, []string{"one", "two"}) {
		t.Fatalf("failed = %#v, order = %#v, err = %v", failed, firstOrder, err)
	}
	secondOrder := []string{}
	second := Orchestrator{
		Store: artifacts, Checkpoints: checkpoints,
		Subagents: Subagents{"worker": scriptedSubagent{order: &secondOrder, fn: func(task TaskSpec, inputs []Artifact) (Artifact, error) {
			if task.ID != "two" || len(inputs) != 1 || string(inputs[0].Data) != `"one"` {
				return Artifact{}, fmt.Errorf("task/inputs = %q/%#v", task.ID, inputs)
			}
			return output(task, task.ID), nil
		}}},
	}
	var events []Event
	report, err := second.Resume(context.Background(), failed.ExecutionID, brief, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || report.Status != StatusCompleted || report.ExecutionID != failed.ExecutionID || report.CheckpointVersion != 3 || !reflect.DeepEqual(secondOrder, []string{"two"}) {
		t.Fatalf("report = %#v, order = %#v, err = %v", report, secondOrder, err)
	}
	if len(report.Tasks) != 2 || report.Tasks[0].TaskID != "one" || report.Tasks[1].TaskID != "two" {
		t.Fatalf("reconstructed tasks = %#v", report.Tasks)
	}
	for index, event := range events {
		if event.ExecutionID != failed.ExecutionID || event.Sequence != int64(index+1) {
			t.Fatalf("resume event %d = %#v", index, event)
		}
	}
	wantEvents := []EventType{EventStarted, EventTaskStarted, EventTaskCompleted, EventCompleted}
	if len(events) != len(wantEvents) {
		t.Fatalf("resume events = %#v", events)
	}
	for index, eventType := range wantEvents {
		if events[index].Type != eventType {
			t.Fatalf("resume event %d = %#v", index, events[index])
		}
	}
}

func TestResumeRejectsChangedBriefAndArtifact(t *testing.T) {
	artifacts, _ := NewInMemoryStore(InMemoryStoreOptions{})
	checkpoints, _ := NewInMemoryCheckpointStore(InMemoryCheckpointOptions{})
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker")}}
	first := Orchestrator{
		Store: artifacts, Checkpoints: checkpoints, IDs: func(prefix string) string { return prefix + "-1" },
		Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
			if task.ID == "two" {
				return Artifact{}, errors.New("stop")
			}
			return output(task, task.ID), nil
		}}},
	}
	failed, _ := first.Run(context.Background(), brief)
	changed := brief
	changed.Goal = "changed"
	_, err := first.Resume(context.Background(), failed.ExecutionID, changed, nil)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != "invalid_checkpoint" {
		t.Fatalf("changed Brief run error = %#v, err = %v", runErr, err)
	}

	snapshot, _, _ := checkpoints.Load(context.Background(), failed.ExecutionID)
	tamperedStore := storeDouble{
		put: artifacts.Put,
		get: func(ctx context.Context, reference ArtifactReference) (Artifact, error) {
			value, err := artifacts.Get(ctx, reference)
			if err == nil {
				value.Data = []byte(`"tampered"`)
			}
			return value, err
		},
	}
	tampered := first
	tampered.Store = tamperedStore
	_, err = tampered.Resume(context.Background(), snapshot.ExecutionID, brief, nil)
	if !errors.As(err, &runErr) || runErr.Code != "checkpoint_artifact_invalid" {
		t.Fatalf("tampered Artifact run error = %#v, err = %v", runErr, err)
	}
}

func TestResumeCompletedSnapshotDoesNotNeedSubagents(t *testing.T) {
	artifacts, _ := NewInMemoryStore(InMemoryStoreOptions{})
	checkpoints, _ := NewInMemoryCheckpointStore(InMemoryCheckpointOptions{})
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	value := Orchestrator{
		Store: artifacts, Checkpoints: checkpoints, IDs: func(prefix string) string { return prefix + "-1" },
		Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}},
	}
	first, err := value.Run(context.Background(), brief)
	if err != nil {
		t.Fatal(err)
	}
	resumer := Orchestrator{Store: artifacts, Checkpoints: checkpoints}
	var events []Event
	report, err := resumer.Resume(context.Background(), first.ExecutionID, brief, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil || report.Status != StatusCompleted || len(report.Tasks) != 1 {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
	want := []EventType{EventStarted, EventCompleted}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for index, event := range events {
		if event.Type != want[index] || event.Sequence != int64(index+1) || event.ExecutionID != first.ExecutionID {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
}

type checkpointStoreDouble struct {
	load func(context.Context, string) (ExecutionSnapshot, bool, error)
	cas  func(context.Context, string, uint64, ExecutionSnapshot) (ExecutionSnapshot, error)
}

func (s checkpointStoreDouble) Load(ctx context.Context, executionID string) (ExecutionSnapshot, bool, error) {
	return s.load(ctx, executionID)
}

func (s checkpointStoreDouble) CompareAndSwap(ctx context.Context, executionID string, expected uint64, next ExecutionSnapshot) (ExecutionSnapshot, error) {
	return s.cas(ctx, executionID, expected, next.Clone())
}

func TestInitialCheckpointFailureStopsBeforeEventsAndSubagent(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	artifacts, _ := NewInMemoryStore(InMemoryStoreOptions{})
	for name, cas := range map[string]func(context.Context, string, uint64, ExecutionSnapshot) (ExecutionSnapshot, error){
		"error": func(context.Context, string, uint64, ExecutionSnapshot) (ExecutionSnapshot, error) {
			return ExecutionSnapshot{}, errors.New("unavailable")
		},
		"panic": func(context.Context, string, uint64, ExecutionSnapshot) (ExecutionSnapshot, error) {
			panic("unavailable")
		},
		"invalid receipt": func(_ context.Context, _ string, _ uint64, next ExecutionSnapshot) (ExecutionSnapshot, error) {
			next.Version++
			return next, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			order := []string{}
			events := []Event{}
			checkpoints := checkpointStoreDouble{
				load: func(context.Context, string) (ExecutionSnapshot, bool, error) { return ExecutionSnapshot{}, false, nil },
				cas:  cas,
			}
			runtime := Orchestrator{
				Store: artifacts, Checkpoints: checkpoints,
				IDs: func(prefix string) string { return prefix + "-1" },
				Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
					return output(task, task.ID), nil
				}}},
			}
			report, err := runtime.RunWithSink(context.Background(), brief, func(event Event) error {
				events = append(events, event)
				return nil
			})
			var runErr *RunError
			if !errors.As(err, &runErr) || runErr.Code != "checkpoint_save_failed" || report.Error == nil || len(order) != 0 || len(events) != 0 {
				t.Fatalf("report = %#v, run error = %#v, order = %#v, events = %#v", report, runErr, order, events)
			}
		})
	}
}

func TestTaskCheckpointFailureDoesNotCommitCompletion(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	artifacts, _ := NewInMemoryStore(InMemoryStoreOptions{})
	persisted, _ := NewInMemoryCheckpointStore(InMemoryCheckpointOptions{})
	checkpoints := checkpointStoreDouble{
		load: persisted.Load,
		cas: func(ctx context.Context, executionID string, expected uint64, next ExecutionSnapshot) (ExecutionSnapshot, error) {
			if expected == 1 {
				return ExecutionSnapshot{}, errors.New("unavailable")
			}
			return persisted.CompareAndSwap(ctx, executionID, expected, next)
		},
	}
	order := []string{}
	var events []Event
	runtime := Orchestrator{
		Store: artifacts, Checkpoints: checkpoints,
		IDs: func(prefix string) string { return prefix + "-1" },
		Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
			return output(task, task.ID), nil
		}}},
	}
	report, err := runtime.RunWithSink(context.Background(), brief, func(event Event) error {
		events = append(events, event)
		return nil
	})
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != "checkpoint_save_failed" || report.CheckpointVersion != 1 || len(report.Tasks) != 1 || report.Tasks[0].Status != TaskFailed {
		t.Fatalf("report = %#v, run error = %#v, err = %v", report, runErr, err)
	}
	snapshot, exists, loadErr := persisted.Load(context.Background(), report.ExecutionID)
	if loadErr != nil || !exists || snapshot.Version != 1 || snapshot.NextTask != 0 || len(snapshot.Completed) != 0 {
		t.Fatalf("snapshot = %#v, exists = %v, err = %v", snapshot, exists, loadErr)
	}
	want := []EventType{EventStarted, EventTaskStarted, EventTaskFailed, EventFailed}
	if len(order) != 1 || len(events) != len(want) {
		t.Fatalf("order = %#v, events = %#v", order, events)
	}
	for index, event := range events {
		if event.Type != want[index] {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
}

func TestResumeCheckpointLoadFailureHasSafeReportError(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	artifacts, _ := NewInMemoryStore(InMemoryStoreOptions{})
	for name, load := range map[string]func(context.Context, string) (ExecutionSnapshot, bool, error){
		"error": func(context.Context, string) (ExecutionSnapshot, bool, error) {
			return ExecutionSnapshot{}, false, errors.New("unavailable")
		},
		"panic": func(context.Context, string) (ExecutionSnapshot, bool, error) {
			panic("unavailable")
		},
	} {
		t.Run(name, func(t *testing.T) {
			checkpoints := checkpointStoreDouble{
				load: load,
				cas: func(context.Context, string, uint64, ExecutionSnapshot) (ExecutionSnapshot, error) {
					return ExecutionSnapshot{}, nil
				},
			}
			report, err := (Orchestrator{Store: artifacts, Checkpoints: checkpoints}).Resume(context.Background(), "execution-1", brief, nil)
			var runErr *RunError
			if !errors.As(err, &runErr) || runErr.Code != "checkpoint_load_failed" || report.Error == nil || report.Error.Code != runErr.Code {
				t.Fatalf("report = %#v, run error = %#v, err = %v", report, runErr, err)
			}
		})
	}
}
