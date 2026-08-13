package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

const (
	DefaultInMemoryStoreMaxObjects = 1024
	DefaultInMemoryStoreMaxBytes   = 64 << 20
)

type InMemoryStoreOptions struct {
	MaxObjects int
	MaxBytes   int64
}

type InMemoryStore struct {
	mu         sync.RWMutex
	maxObjects int
	maxBytes   int64
	bytes      int64
	objects    map[string]Artifact
	keys       map[string]ArtifactReference
}

func NewInMemoryStore(options InMemoryStoreOptions) (*InMemoryStore, error) {
	maximumObjects := options.MaxObjects
	if maximumObjects == 0 {
		maximumObjects = DefaultInMemoryStoreMaxObjects
	}
	maximumBytes := options.MaxBytes
	if maximumBytes == 0 {
		maximumBytes = DefaultInMemoryStoreMaxBytes
	}
	if maximumObjects <= 0 || maximumBytes <= 0 {
		return nil, fmt.Errorf("in-memory artifact store limits must be positive")
	}
	return &InMemoryStore{maxObjects: maximumObjects, maxBytes: maximumBytes, objects: make(map[string]Artifact), keys: make(map[string]ArtifactReference)}, nil
}

func (s *InMemoryStore) Put(ctx context.Context, request PutRequest) (ArtifactReference, error) {
	if ctx == nil || s == nil {
		return ArtifactReference{}, fmt.Errorf("context and in-memory artifact store are required")
	}
	if err := ctx.Err(); err != nil {
		return ArtifactReference{}, err
	}
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return ArtifactReference{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ArtifactReference{}, err
	}
	if existing, ok := s.keys[request.IdempotencyKey]; ok {
		if err := existing.Matches(request.Artifact); err != nil {
			return ArtifactReference{}, fmt.Errorf("artifact idempotency key conflicts with existing content")
		}
		return existing.Clone(), nil
	}
	size := int64(len(request.Artifact.Data))
	objectID := objectID(request)
	if existing, ok := s.objects[objectID]; ok {
		if existing.Digest != request.Artifact.Digest || existing.Name != request.Artifact.Name || existing.MediaType != request.Artifact.MediaType || string(existing.Metadata) != string(request.Artifact.Metadata) {
			return ArtifactReference{}, fmt.Errorf("artifact object identifier collision")
		}
	} else {
		if len(s.objects) >= s.maxObjects {
			return ArtifactReference{}, fmt.Errorf("in-memory artifact object capacity exceeded")
		}
		if size > s.maxBytes-s.bytes {
			return ArtifactReference{}, fmt.Errorf("in-memory artifact byte capacity exceeded")
		}
		s.objects[objectID] = request.Artifact.Clone()
		s.bytes += size
	}
	reference := ReferenceFromArtifact(objectID, request.Artifact)
	s.keys[request.IdempotencyKey] = reference.Clone()
	return reference, nil
}

func (s *InMemoryStore) Get(ctx context.Context, reference ArtifactReference) (Artifact, error) {
	if ctx == nil || s == nil {
		return Artifact{}, fmt.Errorf("context and in-memory artifact store are required")
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	reference = reference.Clone()
	if err := reference.Validate(); err != nil {
		return Artifact{}, err
	}
	s.mu.RLock()
	value, ok := s.objects[reference.ObjectRefID]
	s.mu.RUnlock()
	if !ok {
		return Artifact{}, fmt.Errorf("artifact object is not found")
	}
	value = value.Clone()
	if err := reference.Matches(value); err != nil {
		return Artifact{}, err
	}
	return value, nil
}

func objectID(request PutRequest) string {
	encoded, _ := json.Marshal(struct {
		ExecutionID string          `json:"execution_id"`
		TaskID      string          `json:"task_id"`
		Name        string          `json:"name"`
		MediaType   string          `json:"media_type"`
		Digest      string          `json:"digest"`
		Metadata    json.RawMessage `json:"metadata,omitempty"`
	}{request.ExecutionID, request.TaskID, request.Artifact.Name, request.Artifact.MediaType, request.Artifact.Digest, request.Artifact.Metadata})
	sum := sha256.Sum256(encoded)
	return "objects/" + hex.EncodeToString(sum[:])
}
