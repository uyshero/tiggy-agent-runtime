package memory

import (
	"context"
	"fmt"
)

// Noop explicitly disables recall and observation while satisfying Port.
// It retains no values and performs no external work.
type Noop struct{}

func (Noop) Recall(ctx context.Context, _ RecallRequest) (RecallResult, error) {
	if ctx == nil {
		return RecallResult{}, &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("memory recall context is required")}
	}
	if err := ctx.Err(); err != nil {
		return RecallResult{}, err
	}
	return RecallResult{}, nil
}

func (Noop) Observe(ctx context.Context, _ ObserveRequest) error {
	if ctx == nil {
		return &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("memory observe context is required")}
	}
	return ctx.Err()
}
