package orchestrator

import (
	"fmt"
	"io"

	"github.com/uyshero/tiggy-agent-runtime/internal/strictjson"
)

// ReadBrief decodes exactly one JSON Brief and rejects unknown fields,
// duplicate object keys, or trailing values before validating task order and
// artifact contracts.
func ReadBrief(reader io.Reader) (Brief, error) {
	if reader == nil {
		return Brief{}, fmt.Errorf("brief reader is required")
	}
	var brief Brief
	if err := strictjson.Decode(reader, &brief); err != nil {
		return Brief{}, fmt.Errorf("decode brief: %w", err)
	}
	if err := brief.Validate(); err != nil {
		return Brief{}, fmt.Errorf("validate brief: %w", err)
	}
	return brief, nil
}
