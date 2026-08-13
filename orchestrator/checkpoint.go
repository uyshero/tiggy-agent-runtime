package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	CheckpointProtocolV1 = "tiggy.runtime.orchestrator-checkpoint.v1"
	RuntimeVersionV1     = "tiggy-orchestrator-v1"
)

type CheckpointTask struct {
	TaskID   string            `json:"task_id"`
	Artifact ArtifactReference `json:"artifact"`
}

func (t CheckpointTask) Clone() CheckpointTask {
	t.Artifact = t.Artifact.Clone()
	return t
}

type ExecutionSnapshot struct {
	ProtocolVersion string           `json:"protocol_version"`
	RuntimeVersion  string           `json:"runtime_version"`
	ExecutionID     string           `json:"execution_id"`
	BriefID         string           `json:"brief_id"`
	BriefDigest     string           `json:"brief_digest"`
	Status          Status           `json:"status"`
	NextTask        int              `json:"next_task"`
	Completed       []CheckpointTask `json:"completed,omitempty"`
	ArtifactBytes   int64            `json:"artifact_bytes"`
	Version         uint64           `json:"version"`
}

func (s ExecutionSnapshot) Clone() ExecutionSnapshot {
	completed := s.Completed
	s.Completed = make([]CheckpointTask, len(completed))
	for index, task := range completed {
		s.Completed[index] = task.Clone()
	}
	return s
}

func (s ExecutionSnapshot) Validate(brief Brief) error {
	if err := brief.Validate(); err != nil {
		return err
	}
	digest, err := brief.Digest()
	if err != nil {
		return err
	}
	if s.ProtocolVersion != CheckpointProtocolV1 || s.RuntimeVersion != RuntimeVersionV1 {
		return fmt.Errorf("checkpoint protocol or Runtime version is unsupported")
	}
	if strings.TrimSpace(s.ExecutionID) == "" || s.BriefID != brief.ID || s.BriefDigest != digest {
		return fmt.Errorf("checkpoint execution or Brief identity does not match")
	}
	if s.NextTask < 0 || s.NextTask > len(brief.Tasks) || len(s.Completed) != s.NextTask || s.Version != uint64(s.NextTask)+1 {
		return fmt.Errorf("checkpoint version or task progress is invalid")
	}
	if s.ArtifactBytes < 0 {
		return fmt.Errorf("checkpoint artifact bytes cannot be negative")
	}
	expectedStatus := StatusRunning
	if s.NextTask == len(brief.Tasks) {
		expectedStatus = StatusCompleted
	}
	if s.Status != expectedStatus {
		return fmt.Errorf("checkpoint status does not match task progress")
	}
	var total int64
	for index, completed := range s.Completed {
		if completed.TaskID != brief.Tasks[index].ID {
			return fmt.Errorf("checkpoint task %d does not match the Brief", index)
		}
		if err := completed.Artifact.Validate(); err != nil {
			return fmt.Errorf("checkpoint task %q artifact: %w", completed.TaskID, err)
		}
		if completed.Artifact.Name != brief.Tasks[index].Output.Name || completed.Artifact.MediaType != brief.Tasks[index].Output.MediaType {
			return fmt.Errorf("checkpoint task %q artifact contract changed", completed.TaskID)
		}
		total += completed.Artifact.SizeBytes
		if total < 0 {
			return fmt.Errorf("checkpoint artifact byte total overflow")
		}
	}
	if total != s.ArtifactBytes {
		return fmt.Errorf("checkpoint artifact byte total is inconsistent")
	}
	return nil
}

func (b Brief) Digest() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CheckpointStore persists execution progress without owning scheduling.
//
// Enterprise adapters should be scoped to one authorized execution and, when
// workers are coordinated by a lease, bind CompareAndSwap to the caller's
// current fencing token. The token belongs to the adapter and must not be
// serialized into ExecutionSnapshot. A stale worker must receive an error
// instead of committing progress.
type CheckpointStore interface {
	Load(context.Context, string) (ExecutionSnapshot, bool, error)
	CompareAndSwap(context.Context, string, uint64, ExecutionSnapshot) (ExecutionSnapshot, error)
}

type CheckpointConflictError struct {
	Expected uint64
	Actual   uint64
}

func callCheckpointLoad(ctx context.Context, store CheckpointStore, executionID string) (snapshot ExecutionSnapshot, exists bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("checkpoint Store Load panic: %v", recovered)
		}
	}()
	snapshot, exists, err = store.Load(ctx, executionID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ExecutionSnapshot{}, false, ctxErr
	}
	return snapshot.Clone(), exists, err
}

func callCheckpointCAS(ctx context.Context, store CheckpointStore, executionID string, expected uint64, next ExecutionSnapshot) (snapshot ExecutionSnapshot, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("checkpoint Store CompareAndSwap panic: %v", recovered)
		}
	}()
	snapshot, err = store.CompareAndSwap(ctx, executionID, expected, next.Clone())
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ExecutionSnapshot{}, ctxErr
	}
	return snapshot.Clone(), err
}

func (e *CheckpointConflictError) Error() string {
	if e == nil {
		return "checkpoint version conflict"
	}
	return fmt.Sprintf("checkpoint version conflict: expected %d, actual %d", e.Expected, e.Actual)
}

func initialSnapshot(executionID string, brief Brief) (ExecutionSnapshot, error) {
	digest, err := brief.Digest()
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	return ExecutionSnapshot{
		ProtocolVersion: CheckpointProtocolV1, RuntimeVersion: RuntimeVersionV1,
		ExecutionID: executionID, BriefID: brief.ID, BriefDigest: digest,
		Status: StatusRunning, Version: 1,
	}, nil
}

func validateCheckpointCommit(expected, saved ExecutionSnapshot, brief Brief) error {
	if err := saved.Validate(brief); err != nil {
		return fmt.Errorf("checkpoint Store returned an invalid snapshot: %w", err)
	}
	if expected.ProtocolVersion != saved.ProtocolVersion ||
		expected.RuntimeVersion != saved.RuntimeVersion ||
		expected.ExecutionID != saved.ExecutionID ||
		expected.BriefID != saved.BriefID ||
		expected.BriefDigest != saved.BriefDigest ||
		expected.Status != saved.Status ||
		expected.NextTask != saved.NextTask ||
		expected.ArtifactBytes != saved.ArtifactBytes ||
		expected.Version != saved.Version ||
		len(expected.Completed) != len(saved.Completed) {
		return fmt.Errorf("checkpoint Store returned a snapshot that does not match the commit")
	}
	for index := range expected.Completed {
		left, right := expected.Completed[index], saved.Completed[index]
		if left.TaskID != right.TaskID ||
			left.Artifact.ObjectRefID != right.Artifact.ObjectRefID ||
			left.Artifact.Name != right.Artifact.Name ||
			left.Artifact.MediaType != right.Artifact.MediaType ||
			left.Artifact.SizeBytes != right.Artifact.SizeBytes ||
			left.Artifact.Digest != right.Artifact.Digest ||
			!bytes.Equal(left.Artifact.Metadata, right.Artifact.Metadata) {
			return fmt.Errorf("checkpoint Store returned a snapshot that does not match the commit")
		}
	}
	return nil
}
