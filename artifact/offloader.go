package artifact

import (
	"context"
	"fmt"
	"runtime/debug"
)

const (
	// DefaultMaxInlineBytes 将不超过 64 KiB 的编码 Tool Result 保留在行内。
	DefaultMaxInlineBytes int64 = 64 << 10
	// DefaultMaxArtifactBytes 拒绝超过 16 MiB 的编码 Tool Result。
	DefaultMaxArtifactBytes int64 = 16 << 20
)

// BoundedOffloaderOptions 配置编码字节阈值与存储。
type BoundedOffloaderOptions struct {
	Store            Store
	MaxInlineBytes   int64
	MaxArtifactBytes int64
}

// BoundedOffloader 对超过行内阈值的 payload 精确存储一次。
type BoundedOffloader struct {
	store       Store
	maxInline   int64
	maxArtifact int64
}

// NewBoundedOffloader 校验选项并应用文档约定的默认值。
func NewBoundedOffloader(options BoundedOffloaderOptions) (*BoundedOffloader, error) {
	if options.Store == nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("artifact store is required")}
	}
	maxInline := options.MaxInlineBytes
	if maxInline == 0 {
		maxInline = DefaultMaxInlineBytes
	}
	maxArtifact := options.MaxArtifactBytes
	if maxArtifact == 0 {
		maxArtifact = DefaultMaxArtifactBytes
	}
	if maxInline < 0 || maxArtifact <= maxInline {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("artifact limits must satisfy 0 < inline < artifact")}
	}
	return &BoundedOffloader{store: options.Store, maxInline: maxInline, maxArtifact: maxArtifact}, nil
}

func (o *BoundedOffloader) Offload(ctx context.Context, request Request) (Decision, error) {
	if ctx == nil || o == nil {
		return Decision{}, &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("context and offloader are required")}
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if err := request.Validate(); err != nil {
		return Decision{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	size := int64(len(request.Payload))
	if size <= o.maxInline {
		return Decision{}, nil
	}
	if size > o.maxArtifact {
		return Decision{}, &Error{Code: ErrorPayloadLimit, Cause: fmt.Errorf("tool result payload is %d bytes, maximum is %d", size, o.maxArtifact)}
	}
	stored, err := callStore(ctx, o.store, PutRequest{
		IdempotencyKey: "tool-result:" + request.RunID + ":" + request.CallID,
		CallID:         request.CallID, ToolName: request.ToolName, MediaType: request.MediaType,
		Digest: request.Digest, Data: append([]byte(nil), request.Payload...),
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Decision{}, ctxErr
	}
	if err != nil {
		return Decision{}, &Error{Code: ErrorStoreFailed, Cause: err}
	}
	if err := stored.Validate(); err != nil {
		return Decision{}, &Error{Code: ErrorInvalidResponse, Cause: err}
	}
	decision := Decision{Offloaded: true, Reference: Reference{
		ObjectRefID: stored.ObjectRefID,
		Name:        "tool-result.json",
		MediaType:   request.MediaType,
		SizeBytes:   size,
		Digest:      request.Digest,
	}}
	if err := decision.Validate(request); err != nil {
		return Decision{}, &Error{Code: ErrorInvalidResponse, Cause: err}
	}
	return decision, nil
}

func callStore(ctx context.Context, store Store, request PutRequest) (object StoredObject, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("artifact store panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return store.Put(ctx, request.Clone())
}
