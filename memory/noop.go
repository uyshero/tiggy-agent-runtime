package memory

import (
	"context"
	"fmt"
)

// Noop 显式关闭回忆与观察，同时满足 Port 接口。
// 不保存任何数据，也不发起外部 I/O；适合禁用 Memory 或占位配置。
type Noop struct{}

// Recall 在 Context 有效时返回空结果。
func (Noop) Recall(ctx context.Context, _ RecallRequest) (RecallResult, error) {
	if ctx == nil {
		return RecallResult{}, &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("memory recall context is required")}
	}
	if err := ctx.Err(); err != nil {
		return RecallResult{}, err
	}
	return RecallResult{}, nil
}

// Observe 仅检查 Context，不做任何写入。
func (Noop) Observe(ctx context.Context, _ ObserveRequest) error {
	if ctx == nil {
		return &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("memory observe context is required")}
	}
	return ctx.Err()
}
