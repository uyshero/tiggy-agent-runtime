package orchestrator

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"strings"
)

const (
	DefaultMaxArtifactBytes          int64 = 16 << 20
	DefaultMaxExecutionArtifactBytes int64 = 64 << 20
)

type ArtifactLimits struct {
	MaxArtifactBytes          int64 `json:"max_artifact_bytes"`
	MaxExecutionArtifactBytes int64 `json:"max_execution_artifact_bytes"`
}

func normalizeArtifactLimits(limits ArtifactLimits) (ArtifactLimits, error) {
	if limits.MaxArtifactBytes == 0 {
		limits.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if limits.MaxExecutionArtifactBytes == 0 {
		limits.MaxExecutionArtifactBytes = DefaultMaxExecutionArtifactBytes
	}
	if limits.MaxArtifactBytes <= 0 || limits.MaxExecutionArtifactBytes < limits.MaxArtifactBytes {
		return ArtifactLimits{}, fmt.Errorf("artifact limits require positive execution capacity covering one artifact")
	}
	return limits, nil
}

type ArtifactReference struct {
	ObjectRefID string          `json:"object_ref_id"`
	Name        string          `json:"name"`
	MediaType   string          `json:"media_type"`
	SizeBytes   int64           `json:"size_bytes"`
	Digest      string          `json:"digest"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

func (r ArtifactReference) Clone() ArtifactReference {
	r.Metadata = append(json.RawMessage(nil), r.Metadata...)
	return r
}

func (r ArtifactReference) Validate() error {
	if strings.TrimSpace(r.ObjectRefID) == "" || r.ObjectRefID != strings.TrimSpace(r.ObjectRefID) || len(r.ObjectRefID) > 1024 {
		return fmt.Errorf("artifact object reference id is invalid")
	}
	if strings.Contains(r.ObjectRefID, "://") || strings.ContainsAny(r.ObjectRefID, "?#") {
		return fmt.Errorf("artifact object reference id must be opaque")
	}
	if strings.TrimSpace(r.Name) == "" || r.Name != strings.TrimSpace(r.Name) || len(r.Name) > 512 || strings.TrimSpace(r.MediaType) == "" || r.MediaType != strings.TrimSpace(r.MediaType) || len(r.MediaType) > 255 || r.SizeBytes <= 0 {
		return fmt.Errorf("artifact reference name, media type, and size are required")
	}
	if _, _, err := mime.ParseMediaType(r.MediaType); err != nil {
		return fmt.Errorf("artifact reference media type is invalid: %w", err)
	}
	if !validDigest(r.Digest) {
		return fmt.Errorf("artifact reference digest must be sha256")
	}
	if len(r.Metadata) > 0 && !json.Valid(r.Metadata) {
		return fmt.Errorf("artifact reference metadata must be valid JSON")
	}
	return nil
}

func (r ArtifactReference) Matches(artifact Artifact) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Name != artifact.Name || r.MediaType != artifact.MediaType || r.SizeBytes != int64(len(artifact.Data)) || r.Digest != artifact.Digest {
		return fmt.Errorf("stored artifact does not match its reference")
	}
	if string(r.Metadata) != string(artifact.Metadata) {
		return fmt.Errorf("stored artifact metadata does not match its reference")
	}
	return nil
}

func ReferenceFromArtifact(objectRefID string, artifact Artifact) ArtifactReference {
	return ArtifactReference{
		ObjectRefID: objectRefID, Name: artifact.Name, MediaType: artifact.MediaType,
		SizeBytes: int64(len(artifact.Data)), Digest: artifact.Digest,
		Metadata: append(json.RawMessage(nil), artifact.Metadata...),
	}
}

type PutRequest struct {
	ExecutionID    string   `json:"execution_id"`
	TaskID         string   `json:"task_id"`
	IdempotencyKey string   `json:"idempotency_key"`
	Artifact       Artifact `json:"artifact"`
}

func (r PutRequest) Clone() PutRequest {
	r.Artifact = r.Artifact.Clone()
	return r
}

func (r PutRequest) Validate() error {
	if strings.TrimSpace(r.ExecutionID) == "" || strings.TrimSpace(r.TaskID) == "" || strings.TrimSpace(r.IdempotencyKey) == "" {
		return fmt.Errorf("execution id, task id, and idempotency key are required")
	}
	return validateArtifactValue(r.Artifact)
}

// Store persists Artifact bodies outside Reports and checkpoints. Production
// adapters own authorization, tenant isolation, encryption, retention, and
// idempotency. A worker-scoped adapter may additionally reject writes after its
// Enterprise lease or fencing token is no longer current.
type Store interface {
	Put(context.Context, PutRequest) (ArtifactReference, error)
	Get(context.Context, ArtifactReference) (Artifact, error)
}

func validateArtifactValue(value Artifact) error {
	return value.Validate(ArtifactSpec{Name: value.Name, MediaType: value.MediaType, MaxBytes: int64(len(value.Data))})
}

func validDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}
