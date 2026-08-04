package artifact

import (
	"context"
	"fmt"
	"runtime/debug"
)

const (
	// DefaultMaxInlineBytes keeps encoded Tool Results up to 64 KiB inline.
	DefaultMaxInlineBytes int64 = 64 << 10
	// DefaultMaxArtifactBytes rejects encoded Tool Results over 16 MiB.
	DefaultMaxArtifactBytes int64 = 16 << 20
)

// BoundedOffloaderOptions configures encoded-byte thresholds and storage.
type BoundedOffloaderOptions struct {
	Store            Store
	MaxInlineBytes   int64
	MaxArtifactBytes int64
}

// BoundedOffloader stores payloads over its inline threshold exactly once.
type BoundedOffloader struct {
	store       Store
	maxInline   int64
	maxArtifact int64
}

// NewBoundedOffloader validates options and applies documented defaults.
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
