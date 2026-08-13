// Package artifact 定义大型模型可见值的有界卸载契约。
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/internal/strictjson"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	ProtocolV1          = "tiggy.artifact.tool-result.v1"
	ToolResultMediaType = "application/vnd.tiggy.tool-result+json;version=1"
)

// ToolResultEnvelope 是写入 Artifact Store 的版本化精确值。
type ToolResultEnvelope struct {
	ProtocolVersion string             `json:"protocol_version"`
	Result          message.ToolResult `json:"result"`
}

// Request 是交给 Offloader 的、已编码且由调用方拥有的 Tool Result。
type Request struct {
	RunID     string `json:"run_id"`
	CallID    string `json:"call_id"`
	ToolName  string `json:"tool_name"`
	MediaType string `json:"media_type"`
	Payload   []byte `json:"payload"`
	Digest    string `json:"digest"`
}

func (r Request) Clone() Request {
	r.Payload = append([]byte(nil), r.Payload...)
	return r
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.RunID) == "" || strings.TrimSpace(r.CallID) == "" || strings.TrimSpace(r.ToolName) == "" {
		return fmt.Errorf("run id, call id, and tool name are required")
	}
	if r.MediaType != ToolResultMediaType || len(r.Payload) == 0 {
		return fmt.Errorf("tool result payload and media type are required")
	}
	if digest(r.Payload) != r.Digest {
		return fmt.Errorf("tool result payload digest is inconsistent")
	}
	return nil
}

// Reference binds an opaque Store identifier to exact payload properties.
type Reference struct {
	ObjectRefID string `json:"object_ref_id"`
	Name        string `json:"name,omitempty"`
	MediaType   string `json:"media_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Digest      string `json:"digest"`
}

func (r Reference) Validate() error {
	return r.MessageReference().Validate()
}

func (r Reference) MessageReference() message.ArtifactReference {
	return message.ArtifactReference{
		ObjectRefID: r.ObjectRefID, Name: r.Name, MediaType: r.MediaType,
		SizeBytes: r.SizeBytes, Digest: r.Digest,
	}
}

// Decision keeps a Result inline or replaces it with a validated Reference.
type Decision struct {
	Offloaded bool      `json:"offloaded"`
	Reference Reference `json:"reference,omitempty"`
}

func (d Decision) Validate(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if !d.Offloaded {
		if d.Reference != (Reference{}) {
			return fmt.Errorf("inline decision cannot include an artifact reference")
		}
		return nil
	}
	if err := d.Reference.Validate(); err != nil {
		return err
	}
	if d.Reference.MediaType != request.MediaType || d.Reference.SizeBytes != int64(len(request.Payload)) || d.Reference.Digest != request.Digest {
		return fmt.Errorf("artifact reference does not match source payload")
	}
	return nil
}

// Offloader applies one bounded offload decision and must not be retried implicitly.
type Offloader interface {
	Offload(context.Context, Request) (Decision, error)
}

// PutRequest transfers owned Tool Result bytes to a Store.
type PutRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	CallID         string `json:"call_id"`
	ToolName       string `json:"tool_name"`
	MediaType      string `json:"media_type"`
	Digest         string `json:"digest"`
	Data           []byte `json:"data"`
}

func (r PutRequest) Clone() PutRequest {
	r.Data = append([]byte(nil), r.Data...)
	return r
}

// StoredObject is the opaque identity returned after a successful Put.
type StoredObject struct {
	ObjectRefID string `json:"object_ref_id"`
}

func (o StoredObject) Validate() error {
	probe := message.ArtifactReference{
		ObjectRefID: o.ObjectRefID, MediaType: ToolResultMediaType,
		SizeBytes: 1, Digest: "sha256:" + strings.Repeat("0", 64),
	}
	return probe.Validate()
}

// Store persists Artifact bytes. Implementations define durability and lifecycle.
type Store interface {
	Put(context.Context, PutRequest) (StoredObject, error)
}

// NewRequest encodes a validated successful Tool Result into ProtocolV1.
func NewRequest(runID string, call message.ToolCall, result tool.Result) (Request, error) {
	toolResult := message.ToolResult{
		CallID: call.ID, Name: call.Name, Content: message.CloneContent(result.Content),
		State: append(json.RawMessage(nil), result.State...),
	}
	if err := toolResult.Validate(); err != nil {
		return Request{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	payload, err := json.Marshal(ToolResultEnvelope{ProtocolVersion: ProtocolV1, Result: toolResult})
	if err != nil {
		return Request{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	request := Request{
		RunID: runID, CallID: call.ID, ToolName: call.Name,
		MediaType: ToolResultMediaType, Payload: payload, Digest: digest(payload),
	}
	if err := request.Validate(); err != nil {
		return Request{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	return request.Clone(), nil
}

// DecodeToolResult strictly restores one ProtocolV1 Tool Result envelope.
func DecodeToolResult(payload []byte) (message.ToolResult, error) {
	var envelope ToolResultEnvelope
	if err := strictjson.DecodeBytes(payload, &envelope); err != nil {
		return message.ToolResult{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	if envelope.ProtocolVersion != ProtocolV1 {
		return message.ToolResult{}, &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("unsupported artifact protocol %q", envelope.ProtocolVersion)}
	}
	if err := envelope.Result.Validate(); err != nil {
		return message.ToolResult{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	result := envelope.Result
	result.Content = message.CloneContent(result.Content)
	result.State = append(json.RawMessage(nil), result.State...)
	return result, nil
}

// ApplyDecision returns an owned inline Result or a compact Artifact projection.
func ApplyDecision(original tool.Result, request Request, decision Decision) (tool.Result, error) {
	if err := decision.Validate(request); err != nil {
		return tool.Result{}, &Error{Code: ErrorInvalidDecision, Cause: err}
	}
	if !decision.Offloaded {
		return tool.Result{Content: message.CloneContent(original.Content), State: append(json.RawMessage(nil), original.State...)}, nil
	}
	metadata, err := json.Marshal(struct {
		Offloaded bool      `json:"offloaded"`
		Artifact  Reference `json:"artifact"`
	}{Offloaded: true, Artifact: decision.Reference})
	if err != nil {
		return tool.Result{}, &Error{Code: ErrorInvalidDecision, Cause: err}
	}
	projected := tool.Result{
		Content: []message.Content{{Type: message.ContentArtifact, Artifact: pointer(decision.Reference.MessageReference())}},
		State:   metadata,
	}
	validation := message.ToolResult{CallID: request.CallID, Name: request.ToolName, Content: projected.Content, State: projected.State}
	if err := validation.Validate(); err != nil {
		return tool.Result{}, &Error{Code: ErrorInvalidDecision, Cause: err}
	}
	return projected, nil
}

func digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func pointer[T any](value T) *T {
	return &value
}
