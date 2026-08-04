// Package memory defines scoped long-term recall and observation contracts.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

const (
	DefaultRecallLimit         = 16
	DefaultMaxItemBytes        = 8 << 10
	DefaultMaxRecallBytes      = 64 << 10
	DefaultMaxObservationBytes = 256 << 10
)

// FailureMode controls how an ordinary Port error affects the Agent Run.
type FailureMode string

const (
	FailureModeDegrade FailureMode = "degrade"
	FailureModeFailRun FailureMode = "fail_run"
)

// Scope is an application-defined isolation boundary without Enterprise types.
type Scope struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

func (s Scope) Validate() error {
	if !canonicalBounded(s.Namespace, 128) || !canonicalBounded(s.Key, 512) {
		return fmt.Errorf("memory scope namespace and key are required canonical values")
	}
	return nil
}

// Provenance identifies the application-defined source class of a record.
type Provenance struct {
	Source     string   `json:"source"`
	RunID      string   `json:"run_id,omitempty"`
	MessageIDs []string `json:"message_ids,omitempty"`
}

func (p Provenance) Validate() error {
	if !canonicalBounded(p.Source, 256) {
		return fmt.Errorf("memory provenance source is required")
	}
	if p.RunID != "" && !canonicalBounded(p.RunID, 512) {
		return fmt.Errorf("memory provenance run id is invalid")
	}
	if len(p.MessageIDs) > 64 {
		return fmt.Errorf("memory provenance has too many message ids")
	}
	if len(p.MessageIDs) > 0 && p.RunID == "" {
		return fmt.Errorf("memory provenance message ids require a run id")
	}
	seen := make(map[string]struct{}, len(p.MessageIDs))
	for _, id := range p.MessageIDs {
		if !canonicalBounded(id, 512) {
			return fmt.Errorf("memory provenance message id is invalid")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate memory provenance message id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (p Provenance) clone() Provenance {
	p.MessageIDs = append([]string(nil), p.MessageIDs...)
	return p
}

// Item is one scoped textual memory returned to model context.
type Item struct {
	ID         string     `json:"id"`
	Scope      Scope      `json:"scope"`
	Text       string     `json:"text"`
	Provenance Provenance `json:"provenance"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func (i Item) Validate() error {
	if !canonicalBounded(i.ID, 512) {
		return fmt.Errorf("memory item id is required")
	}
	if err := i.Scope.Validate(); err != nil {
		return err
	}
	if err := i.Provenance.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(i.Text) == "" || !utf8.ValidString(i.Text) {
		return fmt.Errorf("memory item text must be non-empty UTF-8")
	}
	if i.ExpiresAt != nil && (i.ExpiresAt.IsZero() || i.ExpiresAt.Location() != time.UTC) {
		return fmt.Errorf("memory item expiry must be a non-zero UTC timestamp")
	}
	return nil
}

// Clone returns an Item without shared mutable provenance or expiry values.
func (i Item) Clone() Item {
	i.Provenance = i.Provenance.clone()
	if i.ExpiresAt != nil {
		expiresAt := *i.ExpiresAt
		i.ExpiresAt = &expiresAt
	}
	return i
}

// Expired reports whether the Item is unavailable at the given instant.
// Expiry is inclusive: an Item is expired when at >= ExpiresAt.
func (i Item) Expired(at time.Time) bool {
	return i.ExpiresAt != nil && !at.Before(*i.ExpiresAt)
}

func cloneItems(items []Item) []Item {
	if items == nil {
		return nil
	}
	cloned := make([]Item, len(items))
	for index, item := range items {
		cloned[index] = item.Clone()
	}
	return cloned
}

// RecallRequest asks for evidence relevant to the current User input.
type RecallRequest struct {
	RunID string          `json:"run_id"`
	Scope Scope           `json:"scope"`
	Input message.Message `json:"input"`
	Limit int             `json:"limit"`
}

func (r RecallRequest) Clone() RecallRequest {
	r.Input = message.Clone(r.Input)
	return r
}

func (r RecallRequest) Validate() error {
	if !canonicalBounded(r.RunID, 512) {
		return fmt.Errorf("memory recall run id is required")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if err := r.Input.Validate(); err != nil || r.Input.Role != message.RoleUser {
		if err == nil {
			err = fmt.Errorf("memory recall input must have user role")
		}
		return err
	}
	if r.Limit <= 0 {
		return fmt.Errorf("memory recall limit must be positive")
	}
	return nil
}

// RecallResult contains untrusted Items that Core validates before projection.
type RecallResult struct {
	Items []Item `json:"items"`
}

func (r RecallResult) Clone() RecallResult {
	return RecallResult{Items: cloneItems(r.Items)}
}

func (r RecallResult) Validate(request RecallRequest, options Options) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if len(r.Items) > request.Limit || len(r.Items) > options.RecallLimit {
		return fmt.Errorf("memory recall returned too many items")
	}
	seen := make(map[string]struct{}, len(r.Items))
	total := 0
	for index, item := range r.Items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("memory item %d: %w", index, err)
		}
		if item.Scope != request.Scope {
			return fmt.Errorf("memory item %d crossed the requested scope", index)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate memory item id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if len(item.Text) > options.MaxItemBytes {
			return fmt.Errorf("memory item %d exceeds the item limit", index)
		}
		total += len(item.Text)
		if total > options.MaxRecallBytes {
			return fmt.Errorf("memory recall exceeds the total byte limit")
		}
	}
	return nil
}

// ObserveRequest contains only messages created in one completed Agent turn.
type ObserveRequest struct {
	RunID    string            `json:"run_id"`
	Scope    Scope             `json:"scope"`
	Messages []message.Message `json:"messages"`
}

func (r ObserveRequest) Clone() ObserveRequest {
	r.Messages = message.CloneAll(r.Messages)
	return r
}

func (r ObserveRequest) Validate(options Options) error {
	if !canonicalBounded(r.RunID, 512) {
		return fmt.Errorf("memory observe run id is required")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if len(r.Messages) < 2 {
		return fmt.Errorf("memory observation requires a completed turn")
	}
	seen := make(map[string]struct{}, len(r.Messages))
	for index, value := range r.Messages {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("memory observation message %d: %w", index, err)
		}
		if value.Role == message.RoleSystem {
			return fmt.Errorf("memory observation cannot contain System messages")
		}
		if _, exists := seen[value.ID]; exists {
			return fmt.Errorf("duplicate memory observation message id %q", value.ID)
		}
		seen[value.ID] = struct{}{}
	}
	encoded, err := json.Marshal(r.Messages)
	if err != nil {
		return err
	}
	if len(encoded) > options.MaxObservationBytes {
		return fmt.Errorf("memory observation exceeds the byte limit")
	}
	return nil
}

// Port retrieves scoped evidence and observes one completed Agent turn.
// Implementations must be concurrency-safe when their Agent is reused
// concurrently and must honor Context cancellation.
type Port interface {
	Recall(context.Context, RecallRequest) (RecallResult, error)
	Observe(context.Context, ObserveRequest) error
}

// Options bounds data crossing the Port and chooses ordinary-error behavior.
// Zero fields are replaced by DefaultOptions through NormalizeOptions.
type Options struct {
	RecallLimit         int         `json:"recall_limit"`
	MaxItemBytes        int         `json:"max_item_bytes"`
	MaxRecallBytes      int         `json:"max_recall_bytes"`
	MaxObservationBytes int         `json:"max_observation_bytes"`
	RecallFailureMode   FailureMode `json:"recall_failure_mode"`
	ObserveFailureMode  FailureMode `json:"observe_failure_mode"`
}

// DefaultOptions returns the standard bounded, degradable Memory policy.
func DefaultOptions() Options {
	return Options{
		RecallLimit: DefaultRecallLimit, MaxItemBytes: DefaultMaxItemBytes,
		MaxRecallBytes: DefaultMaxRecallBytes, MaxObservationBytes: DefaultMaxObservationBytes,
		RecallFailureMode: FailureModeDegrade, ObserveFailureMode: FailureModeDegrade,
	}
}

// NormalizeOptions fills zero fields and validates all configured boundaries.
func NormalizeOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	if options.RecallLimit == 0 {
		options.RecallLimit = defaults.RecallLimit
	}
	if options.MaxItemBytes == 0 {
		options.MaxItemBytes = defaults.MaxItemBytes
	}
	if options.MaxRecallBytes == 0 {
		options.MaxRecallBytes = defaults.MaxRecallBytes
	}
	if options.MaxObservationBytes == 0 {
		options.MaxObservationBytes = defaults.MaxObservationBytes
	}
	if options.RecallFailureMode == "" {
		options.RecallFailureMode = defaults.RecallFailureMode
	}
	if options.ObserveFailureMode == "" {
		options.ObserveFailureMode = defaults.ObserveFailureMode
	}
	if options.RecallLimit <= 0 || options.MaxItemBytes <= 0 || options.MaxRecallBytes < options.MaxItemBytes || options.MaxObservationBytes <= 0 {
		return Options{}, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("memory limits must be positive and total recall must cover one item")}
	}
	if !validFailureMode(options.RecallFailureMode) || !validFailureMode(options.ObserveFailureMode) {
		return Options{}, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("memory failure mode must be degrade or fail_run")}
	}
	return options, nil
}

func canonicalBounded(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && len(value) <= maximum
}

func validFailureMode(value FailureMode) bool {
	return value == FailureModeDegrade || value == FailureModeFailRun
}
