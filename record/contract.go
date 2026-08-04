// Package record 为 Agent 生命周期事件提供版本化、完整性链接的日志账本。
// 记录从不意味着重新执行模型或 Tool。
package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

const ProtocolV1 = "tiggy.runtime.record.v1"

// Entry 是一条不可变账本记录。
// Digest 认证本条字段；PreviousDigest 链到同一 Run 的前一条 Event。
type Entry struct {
	ProtocolVersion string      `json:"protocol_version"`
	RunID           string      `json:"run_id"`
	PreviousDigest  string      `json:"previous_digest,omitempty"`
	PayloadRedacted bool        `json:"payload_redacted,omitempty"`
	Event           event.Event `json:"event"`
	Digest          string      `json:"digest"`
}

func (e Entry) Clone() Entry {
	cloned := e
	cloned.Event = cloneEvent(e.Event)
	return cloned
}

// Validate 校验单条 Entry 及其可选前驱；不证明 Run 已完成。
// 完整性由 Recording.ValidateComplete 保证。
func (e Entry) Validate(previous *Entry) error {
	if e.ProtocolVersion != ProtocolV1 {
		return fmt.Errorf("unsupported recording protocol %q", e.ProtocolVersion)
	}
	if strings.TrimSpace(e.RunID) == "" || e.RunID != e.Event.RunID {
		return fmt.Errorf("recording and Event run ids must match")
	}
	if err := e.Event.Validate(); err != nil {
		return fmt.Errorf("recorded Event: %w", err)
	}
	if e.Event.ProtocolVersion != event.ProtocolV1 {
		return fmt.Errorf("recording V1 requires Event protocol %q", event.ProtocolV1)
	}
	if previous == nil {
		if e.PreviousDigest != "" {
			return fmt.Errorf("first recording Entry cannot have a previous digest")
		}
		if e.Event.Sequence != 1 || e.Event.Type != event.RunStarted || e.Event.ParentID != "" {
			return fmt.Errorf("recording must begin with an unparented run.started Event at sequence 1")
		}
	} else {
		if previous.RunID != e.RunID {
			return fmt.Errorf("recording Entry changed run id")
		}
		if previous.Event.Terminal() {
			return fmt.Errorf("recording cannot continue after a terminal Event")
		}
		if e.PreviousDigest != previous.Digest {
			return fmt.Errorf("recording previous digest does not match")
		}
		if e.Event.Sequence != previous.Event.Sequence+1 {
			return fmt.Errorf("recording Event sequence is not contiguous")
		}
		if e.Event.Type == event.RunStarted {
			return fmt.Errorf("recording Run cannot start twice")
		}
	}
	want, err := digestEntry(e)
	if err != nil {
		return err
	}
	if !validDigest(e.Digest) || e.Digest != want {
		return fmt.Errorf("recording Entry digest does not match content")
	}
	return nil
}

// Recording contains ordered Entries for one Run. It may be incomplete when
// reading a journal left behind by process interruption.
type Recording struct {
	ProtocolVersion string  `json:"protocol_version"`
	RunID           string  `json:"run_id"`
	Entries         []Entry `json:"entries"`
}

func (r Recording) Clone() Recording {
	cloned := r
	cloned.Entries = make([]Entry, len(r.Entries))
	for index, entry := range r.Entries {
		cloned.Entries[index] = entry.Clone()
	}
	return cloned
}

// Validate verifies the digest chain and generic Event graph invariants. An
// interrupted Recording without a terminal Event is valid but incomplete.
func (r Recording) Validate() error {
	if r.ProtocolVersion != ProtocolV1 {
		return fmt.Errorf("unsupported recording protocol %q", r.ProtocolVersion)
	}
	if strings.TrimSpace(r.RunID) == "" || len(r.Entries) == 0 {
		return fmt.Errorf("recording run id and Entries are required")
	}
	ids := make(map[string]struct{}, len(r.Entries))
	var previous *Entry
	for index := range r.Entries {
		entry := r.Entries[index]
		if entry.RunID != r.RunID {
			return fmt.Errorf("Entry %d changed recording run id", index)
		}
		if err := entry.Validate(previous); err != nil {
			return fmt.Errorf("Entry %d: %w", index, err)
		}
		if _, exists := ids[entry.Event.ID]; exists {
			return fmt.Errorf("Entry %d duplicated Event id %q", index, entry.Event.ID)
		}
		if entry.Event.ParentID != "" {
			if _, exists := ids[entry.Event.ParentID]; !exists {
				return fmt.Errorf("Entry %d references unknown or future parent %q", index, entry.Event.ParentID)
			}
		}
		ids[entry.Event.ID] = struct{}{}
		previous = &r.Entries[index]
	}
	return nil
}

func (r Recording) Complete() bool {
	return len(r.Entries) > 0 && r.Entries[len(r.Entries)-1].Event.Terminal()
}

func (r Recording) ValidateComplete() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if !r.Complete() {
		return fmt.Errorf("recording has no terminal Event")
	}
	return nil
}

// FinalDigest returns the chain head, or an empty string for an empty value.
func (r Recording) FinalDigest() string {
	if len(r.Entries) == 0 {
		return ""
	}
	return r.Entries[len(r.Entries)-1].Digest
}

type digestInput struct {
	ProtocolVersion string      `json:"protocol_version"`
	RunID           string      `json:"run_id"`
	PreviousDigest  string      `json:"previous_digest,omitempty"`
	PayloadRedacted bool        `json:"payload_redacted,omitempty"`
	Event           event.Event `json:"event"`
}

func newEntry(value event.Event, previousDigest string, redacted bool) (Entry, error) {
	entry := Entry{
		ProtocolVersion: ProtocolV1, RunID: value.RunID,
		PreviousDigest: previousDigest, PayloadRedacted: redacted, Event: cloneEvent(value),
	}
	digest, err := digestEntry(entry)
	if err != nil {
		return Entry{}, err
	}
	entry.Digest = digest
	return entry, nil
}

func digestEntry(entry Entry) (string, error) {
	encoded, err := json.Marshal(digestInput{
		ProtocolVersion: entry.ProtocolVersion,
		RunID:           entry.RunID,
		PreviousDigest:  entry.PreviousDigest,
		PayloadRedacted: entry.PayloadRedacted,
		Event:           entry.Event,
	})
	if err != nil {
		return "", fmt.Errorf("encode recording digest input: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneEvent(value event.Event) event.Event {
	cloned := value
	cloned.Payload = append(json.RawMessage(nil), value.Payload...)
	return cloned
}
