package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func BenchmarkReadBrief(b *testing.B) {
	for _, taskCount := range []int{10, 100, 1000} {
		brief := benchmarkBrief(taskCount)
		encoded, err := json.Marshal(brief)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("tasks_%d", taskCount), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(encoded)))
			for range b.N {
				if _, err := ReadBrief(bytes.NewReader(encoded)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkExecutionSnapshotValidate(b *testing.B) {
	for _, taskCount := range []int{10, 100, 1000} {
		brief := benchmarkBrief(taskCount)
		digest, err := brief.Digest()
		if err != nil {
			b.Fatal(err)
		}
		completed := make([]CheckpointTask, taskCount)
		for index, task := range brief.Tasks {
			completed[index] = CheckpointTask{TaskID: task.ID, Artifact: ArtifactReference{
				ObjectRefID: fmt.Sprintf("objects/%d", index), Name: task.Output.Name,
				MediaType: task.Output.MediaType, SizeBytes: 1,
				Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			}}
		}
		snapshot := ExecutionSnapshot{
			ProtocolVersion: CheckpointProtocolV1, RuntimeVersion: RuntimeVersionV1,
			ExecutionID: "execution", BriefID: brief.ID, BriefDigest: digest,
			Status: StatusCompleted, NextTask: taskCount, Completed: completed,
			ArtifactBytes: int64(taskCount), Version: uint64(taskCount) + 1,
		}
		b.Run(fmt.Sprintf("tasks_%d", taskCount), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if err := snapshot.Validate(brief); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkOrchestratorRun100Tasks(b *testing.B) {
	brief := benchmarkBrief(100)
	executor := benchmarkSubagent{}
	runtime := Orchestrator{
		Subagents: Subagents{"worker": executor},
		IDs:       func(prefix string) string { return prefix + "-benchmark" },
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		report, err := runtime.Run(context.Background(), brief)
		if err != nil || report.Status != StatusCompleted || len(report.Tasks) != len(brief.Tasks) {
			b.Fatalf("report status/tasks = %s/%d, err = %v", report.Status, len(report.Tasks), err)
		}
	}
}

type benchmarkSubagent struct{}

func (benchmarkSubagent) Execute(_ context.Context, task TaskSpec, _ []Artifact) (Artifact, error) {
	return NewArtifact(task.Output.Name, task.Output.MediaType, []byte(`{}`)), nil
}

func benchmarkBrief(taskCount int) Brief {
	tasks := make([]TaskSpec, taskCount)
	for index := range tasks {
		id := fmt.Sprintf("task-%04d", index)
		tasks[index] = TaskSpec{
			ID: id, Objective: "execute " + id, Subagent: "worker",
			Output: ArtifactSpec{Name: id + ".json", MediaType: "application/json"},
		}
	}
	return Brief{ID: "benchmark-brief", Goal: "benchmark deterministic orchestration", Tasks: tasks}
}
