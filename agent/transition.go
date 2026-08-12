package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

// TransitionProtocolVersion versions the durable checkpoint hook contract.
const TransitionProtocolVersion = "1"

// TransitionType identifies a durable execution boundary.
type TransitionType string

const (
	TransitionModelCallPrepared           TransitionType = "model_call.prepared"
	TransitionModelCallCompleted          TransitionType = "model_call.completed"
	TransitionToolBatchFrozen             TransitionType = "tool_batch.frozen"
	TransitionToolEffectPrepared          TransitionType = "tool_effect.prepared"
	TransitionToolEffectStarted           TransitionType = "tool_effect.started"
	TransitionToolEffectCompleted         TransitionType = "tool_effect.completed"
	TransitionToolEffectIndeterminate     TransitionType = "tool_effect.indeterminate"
	TransitionStateCommitted              TransitionType = "state_transition.committed"
	TransitionResumeContinuationConsumed  TransitionType = "resume_continuation.consumed"
	TransitionRecoveryContinuationApplied TransitionType = "recovery_continuation.applied"
)

// ToolEffectClass describes the strongest recovery guarantee declared by an
// application for a Tool. Undeclared Tools use the conservative default.
type ToolEffectClass string

const (
	ToolEffectReadOnly             ToolEffectClass = "read_only"
	ToolEffectIdempotentWrite      ToolEffectClass = "idempotent_write"
	ToolEffectReconcilableWrite    ToolEffectClass = "reconcilable_write"
	ToolEffectNonReconcilableWrite ToolEffectClass = "non_reconcilable_write"
)

func (c ToolEffectClass) validate() error {
	switch c {
	case ToolEffectReadOnly, ToolEffectIdempotentWrite, ToolEffectReconcilableWrite, ToolEffectNonReconcilableWrite:
		return nil
	default:
		return fmt.Errorf("unsupported Tool effect class %q", c)
	}
}

// TransitionToolBinding contains content-free recovery metadata for one Tool
// call. Arguments and results are represented only by digests.
type TransitionToolBinding struct {
	CallID          string          `json:"call_id"`
	ToolDigest      string          `json:"tool_digest"`
	ArgumentsDigest string          `json:"arguments_digest"`
	EffectClass     ToolEffectClass `json:"effect_class"`
	IdempotencyKey  string          `json:"idempotency_key"`
}

// ToolExecutionTransition is the content-free transition metadata available to
// a Tool during Execute. Adapters can forward IdempotencyKey to external APIs.
type ToolExecutionTransition struct {
	RunID           string
	CallID          string
	ToolDigest      string
	ArgumentsDigest string
	EffectClass     ToolEffectClass
	IdempotencyKey  string
}

type toolExecutionTransitionContextKey struct{}

// ToolTransitionFromContext returns durable effect metadata injected by the
// Agent. Direct Toolkit calls that bypass Agent execution do not contain it.
func ToolTransitionFromContext(ctx context.Context) (ToolExecutionTransition, bool) {
	value, ok := ctx.Value(toolExecutionTransitionContextKey{}).(ToolExecutionTransition)
	return value, ok
}

func withToolExecutionTransition(ctx context.Context, runID string, binding TransitionToolBinding) context.Context {
	return context.WithValue(ctx, toolExecutionTransitionContextKey{}, ToolExecutionTransition{
		RunID:           runID,
		CallID:          binding.CallID,
		ToolDigest:      binding.ToolDigest,
		ArgumentsDigest: binding.ArgumentsDigest,
		EffectClass:     binding.EffectClass,
		IdempotencyKey:  binding.IdempotencyKey,
	})
}

// TransitionPrivateData carries sensitive recovery material to the Hook
// without placing it in JSON metadata. A platform is responsible for tenant
// isolation and encryption before persistence.
type TransitionPrivateData struct {
	ModelRequest         *model.Request  `json:"-"`
	ModelResponse        *model.Response `json:"-"`
	ToolArguments        json.RawMessage `json:"-"`
	ToolResult           *tool.Result    `json:"-"`
	ToolResultStateReady bool            `json:"-"`
	ToolResultIsError    bool            `json:"-"`
	ToolResultRetryable  bool            `json:"-"`
}

func (d TransitionPrivateData) clone() TransitionPrivateData {
	cloned := d
	if d.ModelRequest != nil {
		value := *d.ModelRequest
		value.Messages = message.CloneAll(d.ModelRequest.Messages)
		value.Tools = cloneToolDefinitions(d.ModelRequest.Tools)
		value.Parameters = append(json.RawMessage(nil), d.ModelRequest.Parameters...)
		cloned.ModelRequest = &value
	}
	if d.ModelResponse != nil {
		value := *d.ModelResponse
		value.Message = message.Clone(d.ModelResponse.Message)
		cloned.ModelResponse = &value
	}
	cloned.ToolArguments = append(json.RawMessage(nil), d.ToolArguments...)
	if d.ToolResult != nil {
		value := *d.ToolResult
		value.Content = message.CloneContent(d.ToolResult.Content)
		value.State = append(json.RawMessage(nil), d.ToolResult.State...)
		cloned.ToolResult = &value
	}
	return cloned
}

func (b TransitionToolBinding) validate() error {
	if strings.TrimSpace(b.CallID) == "" || strings.TrimSpace(b.ToolDigest) == "" ||
		strings.TrimSpace(b.ArgumentsDigest) == "" || strings.TrimSpace(b.IdempotencyKey) == "" {
		return fmt.Errorf("Tool transition binding fields are required")
	}
	return b.EffectClass.validate()
}

// Transition is delivered synchronously to TransitionHook before the Runtime
// crosses or commits the described boundary. Checkpoint is an owned State copy
// for application-managed encrypted persistence and is intentionally excluded
// from JSON so callers do not accidentally log sensitive message content.
type Transition struct {
	ProtocolVersion string         `json:"protocol_version"`
	ID              string         `json:"id"`
	RunID           string         `json:"run_id"`
	Sequence        int64          `json:"sequence"`
	CreatedAt       time.Time      `json:"created_at"`
	Type            TransitionType `json:"type"`
	StateDigest     string         `json:"state_digest"`

	ParentTransitionID string                  `json:"parent_transition_id,omitempty"`
	CallID             string                  `json:"call_id,omitempty"`
	BatchDigest        string                  `json:"batch_digest,omitempty"`
	ContinuationID     string                  `json:"continuation_id,omitempty"`
	IdempotencyKey     string                  `json:"idempotency_key,omitempty"`
	Outcome            string                  `json:"outcome,omitempty"`
	Cause              string                  `json:"cause,omitempty"`
	Tools              []TransitionToolBinding `json:"tools,omitempty"`

	Checkpoint State                 `json:"-"`
	Private    TransitionPrivateData `json:"-"`
}

// Clone returns an owned copy safe for retention by a Hook implementation.
func (t Transition) Clone() Transition {
	cloned := t
	cloned.Tools = append([]TransitionToolBinding(nil), t.Tools...)
	cloned.Checkpoint = t.Checkpoint.Clone()
	cloned.Private = t.Private.clone()
	return cloned
}

// Validate checks the content-free transition envelope and type-specific keys.
func (t Transition) Validate() error {
	if t.ProtocolVersion != TransitionProtocolVersion || strings.TrimSpace(t.ID) == "" ||
		strings.TrimSpace(t.RunID) == "" || t.Sequence <= 0 || t.CreatedAt.IsZero() || t.Type == "" {
		return fmt.Errorf("invalid Transition envelope")
	}
	if !validSHA256Digest(t.StateDigest) {
		return fmt.Errorf("State digest must be sha256")
	}
	checkpointDigest, err := t.Checkpoint.Digest()
	if err != nil {
		return fmt.Errorf("invalid checkpoint State: %w", err)
	}
	if checkpointDigest != t.StateDigest {
		return fmt.Errorf("checkpoint State does not match digest")
	}
	for index, binding := range t.Tools {
		if err := binding.validate(); err != nil {
			return fmt.Errorf("Tool binding %d: %w", index, err)
		}
	}
	switch t.Type {
	case TransitionModelCallPrepared:
		if strings.TrimSpace(t.CallID) == "" || strings.TrimSpace(t.IdempotencyKey) == "" {
			return fmt.Errorf("model transition requires Call ID and idempotency key")
		}
	case TransitionModelCallCompleted:
		if strings.TrimSpace(t.CallID) == "" || strings.TrimSpace(t.IdempotencyKey) == "" ||
			strings.TrimSpace(t.ParentTransitionID) == "" || strings.TrimSpace(t.Outcome) == "" {
			return fmt.Errorf("completed model transition requires Call ID, parent, outcome, and idempotency key")
		}
	case TransitionToolBatchFrozen:
		if !validSHA256Digest(t.BatchDigest) || len(t.Tools) == 0 {
			return fmt.Errorf("frozen Tool batch requires digest and bindings")
		}
		digest, err := digestTransitionBindings(t.Tools)
		if err != nil || digest != t.BatchDigest {
			return fmt.Errorf("frozen Tool batch does not match digest")
		}
	case TransitionToolEffectPrepared, TransitionToolEffectStarted, TransitionToolEffectCompleted, TransitionToolEffectIndeterminate:
		if len(t.Tools) != 1 {
			return fmt.Errorf("Tool effect transition requires exactly one binding")
		}
		if t.CallID != t.Tools[0].CallID || t.IdempotencyKey != t.Tools[0].IdempotencyKey || strings.TrimSpace(t.Outcome) == "" {
			return fmt.Errorf("Tool effect transition does not match its binding")
		}
		if t.Type != TransitionToolEffectPrepared && strings.TrimSpace(t.ParentTransitionID) == "" {
			return fmt.Errorf("started or terminal Tool effect transition requires parent")
		}
	case TransitionStateCommitted:
		if strings.TrimSpace(t.Cause) == "" {
			return fmt.Errorf("State commit transition requires cause")
		}
	case TransitionResumeContinuationConsumed:
		if strings.TrimSpace(t.ContinuationID) == "" || strings.TrimSpace(t.IdempotencyKey) == "" {
			return fmt.Errorf("Resume transition requires continuation ID and idempotency key")
		}
	case TransitionRecoveryContinuationApplied:
		if strings.TrimSpace(t.ParentTransitionID) == "" || strings.TrimSpace(t.ContinuationID) == "" ||
			strings.TrimSpace(t.IdempotencyKey) == "" || strings.TrimSpace(t.Outcome) == "" ||
			t.ParentTransitionID != t.ContinuationID {
			return fmt.Errorf("Recovery transition requires matching source, parent, outcome, and idempotency key")
		}
		if t.Outcome != string(recoveryModelResponse) && t.Outcome != string(recoveryToolResult) {
			return fmt.Errorf("Recovery transition has unsupported outcome %q", t.Outcome)
		}
		if t.IdempotencyKey != stableTransitionKey("recovery-continuation", t.ContinuationID) {
			return fmt.Errorf("Recovery transition idempotency key does not match source")
		}
	default:
		return fmt.Errorf("unsupported Transition type %q", t.Type)
	}
	return nil
}

// TransitionHook durably records a Transition. Implementations must be safe
// for concurrent Runs. Returning an error stops Runtime progression.
type TransitionHook interface {
	CommitTransition(context.Context, Transition) error
}

// TransitionHookFunc adapts a function to TransitionHook.
type TransitionHookFunc func(context.Context, Transition) error

func (f TransitionHookFunc) CommitTransition(ctx context.Context, value Transition) error {
	return f(ctx, value)
}

type transitionEmitter struct {
	hook     TransitionHook
	runID    string
	ids      IDGenerator
	now      Clock
	sequence int64
}

type transitionFields struct {
	parentTransitionID string
	callID             string
	batchDigest        string
	continuationID     string
	idempotencyKey     string
	outcome            string
	cause              string
	tools              []TransitionToolBinding
	private            TransitionPrivateData
}

func (e *transitionEmitter) commit(ctx context.Context, kind TransitionType, state State, fields transitionFields) (Transition, error) {
	if e == nil || e.hook == nil {
		return Transition{}, nil
	}
	digest, err := state.Digest()
	if err != nil {
		return Transition{}, fmt.Errorf("digest checkpoint State: %w", err)
	}
	e.sequence++
	value := Transition{
		ProtocolVersion: TransitionProtocolVersion,
		ID:              e.ids("transition"),
		RunID:           e.runID,
		Sequence:        e.sequence,
		CreatedAt:       e.now().UTC(),
		Type:            kind,
		StateDigest:     digest,

		ParentTransitionID: fields.parentTransitionID,
		CallID:             fields.callID,
		BatchDigest:        fields.batchDigest,
		ContinuationID:     fields.continuationID,
		IdempotencyKey:     fields.idempotencyKey,
		Outcome:            fields.outcome,
		Cause:              fields.cause,
		Tools:              append([]TransitionToolBinding(nil), fields.tools...),
		Checkpoint:         state.Clone(),
		Private:            fields.private.clone(),
	}
	if err := value.Validate(); err != nil {
		return Transition{}, err
	}
	if err := callTransitionHook(ctx, e.hook, value.Clone()); err != nil {
		return value, err
	}
	return value, nil
}

func callTransitionHook(ctx context.Context, hook TransitionHook, value Transition) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("Transition Hook panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return hook.CommitTransition(ctx, value)
}

func stableTransitionKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func digestTransitionBindings(bindings []TransitionToolBinding) (string, error) {
	encoded, err := json.Marshal(bindings)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func transitionRunError(code string, cause error) *RunError {
	return &RunError{
		Class: ErrorTransition, Code: code,
		SafeMessage: "The Agent could not persist its execution transition.", Cause: cause,
	}
}
