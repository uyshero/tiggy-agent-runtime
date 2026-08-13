package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"strings"
	"unicode/utf8"
)

const DefaultMaxProjectedArtifactBytes int64 = 256 << 10

// InputProjector converts verified input artifacts into model-visible task
// text. Implementations must treat artifact bytes as untrusted data.
type InputProjector interface {
	Project(TaskSpec, []Artifact) (string, error)
}

// TextInputProjector safely serializes bounded UTF-8 text and JSON artifacts.
type TextInputProjector struct {
	MaxBytes int64
}

func (p TextInputProjector) Project(task TaskSpec, inputs []Artifact) (string, error) {
	maximum := p.MaxBytes
	if maximum == 0 {
		maximum = DefaultMaxProjectedArtifactBytes
	}
	if maximum < 0 {
		return "", errors.New("projected artifact byte limit cannot be negative")
	}
	var total int64
	projected := make([]projectedArtifact, len(inputs))
	for i, input := range inputs {
		if input.Digest != digest(input.Data) {
			return "", fmt.Errorf("input artifact %q digest does not match data", input.Name)
		}
		total += int64(len(input.Data))
		if total > maximum {
			return "", fmt.Errorf("input artifacts exceed projection limit of %d bytes", maximum)
		}
		mediaType, _, err := mime.ParseMediaType(input.MediaType)
		if err != nil || !projectableMediaType(mediaType) {
			return "", fmt.Errorf("input artifact %q has unsupported media type %q", input.Name, input.MediaType)
		}
		if !utf8.Valid(input.Data) {
			return "", fmt.Errorf("input artifact %q is not valid UTF-8", input.Name)
		}
		if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
			if !json.Valid(input.Data) {
				return "", fmt.Errorf("input artifact %q is not valid JSON", input.Name)
			}
		}
		projected[i] = projectedArtifact{Name: input.Name, MediaType: input.MediaType, Digest: input.Digest, Content: string(bytes.Clone(input.Data))}
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return "", fmt.Errorf("encode input artifacts: %w", err)
	}
	return string(encoded), nil
}

type projectedArtifact struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	Content   string `json:"content"`
}

func projectableMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
