package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const DefaultInMemoryCheckpointMaxExecutions = 1024

type InMemoryCheckpointOptions struct {
	MaxExecutions int
}

type InMemoryCheckpointStore struct {
	mu            sync.RWMutex
	maxExecutions int
	executions    map[string]ExecutionSnapshot
}

func NewInMemoryCheckpointStore(options InMemoryCheckpointOptions) (*InMemoryCheckpointStore, error) {
	maximum := options.MaxExecutions
	if maximum == 0 {
		maximum = DefaultInMemoryCheckpointMaxExecutions
	}
	if maximum <= 0 {
		return nil, fmt.Errorf("in-memory checkpoint execution limit must be positive")
	}
	return &InMemoryCheckpointStore{maxExecutions: maximum, executions: make(map[string]ExecutionSnapshot)}, nil
}

func (s *InMemoryCheckpointStore) Load(ctx context.Context, executionID string) (ExecutionSnapshot, bool, error) {
	if ctx == nil || s == nil || strings.TrimSpace(executionID) == "" {
		return ExecutionSnapshot{}, false, fmt.Errorf("context, checkpoint Store, and execution id are required")
	}
	if err := ctx.Err(); err != nil {
		return ExecutionSnapshot{}, false, err
	}
	s.mu.RLock()
	value, exists := s.executions[executionID]
	s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return ExecutionSnapshot{}, false, err
	}
	return value.Clone(), exists, nil
}

func (s *InMemoryCheckpointStore) CompareAndSwap(ctx context.Context, executionID string, expected uint64, next ExecutionSnapshot) (ExecutionSnapshot, error) {
	if ctx == nil || s == nil || strings.TrimSpace(executionID) == "" {
		return ExecutionSnapshot{}, fmt.Errorf("context, checkpoint Store, and execution id are required")
	}
	if err := ctx.Err(); err != nil {
		return ExecutionSnapshot{}, err
	}
	next = next.Clone()
	if next.ExecutionID != executionID || next.Version != expected+1 {
		return ExecutionSnapshot{}, fmt.Errorf("checkpoint identity or next version is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ExecutionSnapshot{}, err
	}
	current, exists := s.executions[executionID]
	actual := uint64(0)
	if exists {
		actual = current.Version
	}
	if actual != expected {
		return ExecutionSnapshot{}, &CheckpointConflictError{Expected: expected, Actual: actual}
	}
	if !exists && len(s.executions) >= s.maxExecutions {
		return ExecutionSnapshot{}, fmt.Errorf("in-memory checkpoint execution capacity exceeded")
	}
	s.executions[executionID] = next.Clone()
	return next.Clone(), nil
}
