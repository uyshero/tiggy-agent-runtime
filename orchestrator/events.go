package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type EventType string

const (
	EventStarted          EventType = "orchestrator.started"
	EventCompleted        EventType = "orchestrator.completed"
	EventFailed           EventType = "orchestrator.failed"
	EventCanceled         EventType = "orchestrator.canceled"
	EventTaskStarted      EventType = "task.started"
	EventTaskCompleted    EventType = "task.completed"
	EventTaskFailed       EventType = "task.failed"
	EventTaskVerification EventType = "task.verification_failed"
	EventTaskCanceled     EventType = "task.canceled"
	EventTaskSkipped      EventType = "task.skipped"
)

// Event is a content-free task lifecycle envelope. Sequence starts at one
// and is monotonic within one InvocationID.
type Event struct {
	ProtocolVersion string        `json:"protocol_version"`
	ID              string        `json:"id"`
	ExecutionID     string        `json:"execution_id"`
	InvocationID    string        `json:"invocation_id,omitempty"`
	BriefID         string        `json:"brief_id"`
	TaskID          string        `json:"task_id,omitempty"`
	Type            EventType     `json:"type"`
	Sequence        int64         `json:"sequence"`
	CreatedAt       time.Time     `json:"created_at"`
	Status          string        `json:"status,omitempty"`
	ArtifactDigest  string        `json:"artifact_digest,omitempty"`
	Error           *ErrorPayload `json:"error,omitempty"`
}

const (
	// ProtocolV1 is retained for validating events emitted by Runtime v0.2.2.
	ProtocolV1 = "tiggy.runtime.orchestrator-event.v1"
	ProtocolV2 = "tiggy.runtime.orchestrator-event.v2"
)

func (e Event) Payload() ([]byte, error) { return json.Marshal(e) }

func (e Event) Validate() error {
	if e.ProtocolVersion != ProtocolV1 && e.ProtocolVersion != ProtocolV2 {
		return fmt.Errorf("unsupported event protocol %q", e.ProtocolVersion)
	}
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.ExecutionID) == "" || strings.TrimSpace(e.BriefID) == "" {
		return fmt.Errorf("event protocol, id, execution id, and brief id are required")
	}
	if e.ProtocolVersion == ProtocolV2 && strings.TrimSpace(e.InvocationID) == "" {
		return fmt.Errorf("event invocation id is required by protocol v2")
	}
	if e.Sequence <= 0 || e.Type == "" || e.CreatedAt.IsZero() {
		return fmt.Errorf("event sequence, type, and timestamp are required")
	}
	return nil
}

type Sink func(Event) error
