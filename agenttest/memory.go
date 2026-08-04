package agenttest

import (
	"context"

	"github.com/uyshero/tiggy-agent-runtime/memory"
)

// FuncMemory is a compact Memory Port test double. Requests are cloned before
// they reach callbacks so tests exercise the same ownership boundary as Agent.
type FuncMemory struct {
	RecallFunc  func(context.Context, memory.RecallRequest) (memory.RecallResult, error)
	ObserveFunc func(context.Context, memory.ObserveRequest) error
}

func (f FuncMemory) Recall(ctx context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
	if f.RecallFunc == nil {
		return memory.RecallResult{}, nil
	}
	return f.RecallFunc(ctx, request.Clone())
}

func (f FuncMemory) Observe(ctx context.Context, request memory.ObserveRequest) error {
	if f.ObserveFunc == nil {
		return nil
	}
	return f.ObserveFunc(ctx, request.Clone())
}
