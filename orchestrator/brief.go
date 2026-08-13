package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
)

// ReadBrief decodes exactly one JSON Brief and rejects unknown fields or
// trailing values before validating task order and artifact contracts.
func ReadBrief(reader io.Reader) (Brief, error) {
	if reader == nil {
		return Brief{}, fmt.Errorf("brief reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var brief Brief
	if err := decoder.Decode(&brief); err != nil {
		return Brief{}, fmt.Errorf("decode brief: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Brief{}, fmt.Errorf("brief must contain exactly one JSON value")
		}
		return Brief{}, fmt.Errorf("brief has trailing data: %w", err)
	}
	if err := brief.Validate(); err != nil {
		return Brief{}, fmt.Errorf("validate brief: %w", err)
	}
	return brief, nil
}
