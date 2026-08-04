package artifact_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/artifact"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

func TestToolResultArtifactRoundTrip(t *testing.T) {
	t.Parallel()

	original := tool.Result{
		Content: []message.Content{{Type: message.ContentText, Text: "large result"}},
		State:   []byte(`{"cursor":2}`),
	}
	request, err := artifact.NewRequest("run_1", toolCall(), original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := artifact.DecodeToolResult(request.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CallID != "call_1" || decoded.Name != "report" || decoded.Content[0].Text != "large result" || string(decoded.State) != `{"cursor":2}` {
		t.Fatalf("decoded Tool Result = %#v", decoded)
	}
	original.Content[0].Text = "mutated"
	decodedAgain, err := artifact.DecodeToolResult(request.Payload)
	if err != nil || decodedAgain.Content[0].Text != "large result" {
		t.Fatal("Artifact Request aliases the source Tool Result")
	}
}

func TestBoundedOffloaderKeepsSmallResultInline(t *testing.T) {
	t.Parallel()

	storeCalls := 0
	offloader := newOffloader(t, storeFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
		storeCalls++
		return artifact.StoredObject{ObjectRefID: "objects/unexpected"}, nil
	}), 4096, 8192)
	request := newRequest(t, "small")
	decision, err := offloader.Offload(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Offloaded || storeCalls != 0 {
		t.Fatalf("inline decision = %#v, store calls = %d", decision, storeCalls)
	}
	projected, err := artifact.ApplyDecision(tool.TextResult("small"), request, decision)
	if err != nil || projected.Content[0].Text != "small" {
		t.Fatalf("inline projection = %#v, error = %v", projected, err)
	}
}

func TestBoundedOffloaderStoresOwnedPayloadAndBuildsReference(t *testing.T) {
	t.Parallel()

	var stored artifact.PutRequest
	offloader := newOffloader(t, storeFunc(func(_ context.Context, request artifact.PutRequest) (artifact.StoredObject, error) {
		stored = request.Clone()
		request.Data[0] = 'x'
		return artifact.StoredObject{ObjectRefID: "objects/result-1"}, nil
	}), 1, 4096)
	original := tool.TextResult(strings.Repeat("result", 20))
	request, err := artifact.NewRequest("run_1", toolCall(), original)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := offloader.Offload(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Offloaded || decision.Reference.ObjectRefID != "objects/result-1" || decision.Reference.Digest != request.Digest || decision.Reference.SizeBytes != int64(len(request.Payload)) {
		t.Fatalf("decision = %#v", decision)
	}
	if stored.IdempotencyKey != "tool-result:run_1:call_1" || stored.Digest != request.Digest || string(stored.Data) != string(request.Payload) {
		t.Fatalf("Put Request = %#v", stored)
	}
	projected, err := artifact.ApplyDecision(original, request, decision)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Content) != 1 || projected.Content[0].Artifact == nil || projected.Content[0].Artifact.ObjectRefID != "objects/result-1" {
		t.Fatalf("projected Tool Result = %#v", projected)
	}
	decoded, err := artifact.DecodeToolResult(stored.Data)
	if err != nil || decoded.Content[0].Text != original.Content[0].Text {
		t.Fatalf("stored artifact cannot restore result: %#v, %v", decoded, err)
	}
}

func TestBoundedOffloaderContainsLimitsStoreFailuresAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("payload limit", func(t *testing.T) {
		offloader := newOffloader(t, storeFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
			t.Fatal("store called over artifact limit")
			return artifact.StoredObject{}, nil
		}), 1, 64)
		_, err := offloader.Offload(context.Background(), newRequest(t, strings.Repeat("x", 256)))
		assertArtifactCode(t, err, artifact.ErrorPayloadLimit)
	})

	for _, test := range []struct {
		name  string
		store artifact.Store
		code  artifact.ErrorCode
	}{
		{name: "error", store: storeFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
			return artifact.StoredObject{}, errors.New("offline")
		}), code: artifact.ErrorStoreFailed},
		{name: "panic", store: storeFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
			panic("boom")
		}), code: artifact.ErrorStoreFailed},
		{name: "invalid response", store: storeFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
			return artifact.StoredObject{ObjectRefID: "https://signed.example/result"}, nil
		}), code: artifact.ErrorInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			offloader := newOffloader(t, test.store, 1, 4096)
			_, err := offloader.Offload(context.Background(), newRequest(t, strings.Repeat("x", 32)))
			assertArtifactCode(t, err, test.code)
		})
	}

	t.Run("cancellation after store", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		offloader := newOffloader(t, storeFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
			cancel()
			return artifact.StoredObject{ObjectRefID: "objects/possibly-stored"}, nil
		}), 1, 4096)
		_, err := offloader.Offload(ctx, newRequest(t, strings.Repeat("x", 32)))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
}

func TestApplyDecisionRejectsMismatchedReference(t *testing.T) {
	t.Parallel()

	request := newRequest(t, "result")
	_, err := artifact.ApplyDecision(tool.TextResult("result"), request, artifact.Decision{
		Offloaded: true,
		Reference: artifact.Reference{
			ObjectRefID: "objects/result", MediaType: request.MediaType,
			SizeBytes: int64(len(request.Payload)) + 1, Digest: request.Digest,
		},
	})
	assertArtifactCode(t, err, artifact.ErrorInvalidDecision)
}

type storeFunc func(context.Context, artifact.PutRequest) (artifact.StoredObject, error)

func (f storeFunc) Put(ctx context.Context, request artifact.PutRequest) (artifact.StoredObject, error) {
	return f(ctx, request.Clone())
}

func newOffloader(t *testing.T, store artifact.Store, inline, maximum int64) *artifact.BoundedOffloader {
	t.Helper()
	value, err := artifact.NewBoundedOffloader(artifact.BoundedOffloaderOptions{
		Store: store, MaxInlineBytes: inline, MaxArtifactBytes: maximum,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newRequest(t *testing.T, text string) artifact.Request {
	t.Helper()
	request, err := artifact.NewRequest("run_1", toolCall(), tool.TextResult(text))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func toolCall() message.ToolCall {
	return message.ToolCall{ID: "call_1", Name: "report", Arguments: []byte(`{}`)}
}

func assertArtifactCode(t *testing.T, err error, code artifact.ErrorCode) {
	t.Helper()
	var value *artifact.Error
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want artifact code %q", err, code)
	}
}
