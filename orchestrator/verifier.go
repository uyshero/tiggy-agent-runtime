package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime/debug"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/uyshero/tiggy-agent-runtime/internal/strictjson"
)

const (
	VerifierJSONSchema = "json_schema"
	VerifierText       = "text"
)

type VerificationSpec struct {
	Verifier string          `json:"verifier"`
	Config   json.RawMessage `json:"config,omitempty"`
}

func (s VerificationSpec) Clone() VerificationSpec {
	s.Config = append(json.RawMessage(nil), s.Config...)
	return s
}

func (s VerificationSpec) Validate() error {
	if strings.TrimSpace(s.Verifier) == "" || s.Verifier != strings.TrimSpace(s.Verifier) {
		return fmt.Errorf("verifier name is required without surrounding whitespace")
	}
	if len(s.Config) > 0 {
		var document any
		if err := strictjson.DecodeBytes(s.Config, &document); err != nil {
			return fmt.Errorf("verifier config must contain one unambiguous JSON value: %w", err)
		}
	}
	return nil
}

type Verifiers map[string]Verifier

func BuiltinVerifiers() Verifiers {
	return Verifiers{VerifierJSONSchema: JSONSchemaVerifier{}, VerifierText: TextVerifier{}}
}

type JSONSchemaVerifier struct{}

func (JSONSchemaVerifier) Verify(ctx context.Context, task TaskSpec, output Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if task.Verification == nil || len(task.Verification.Config) == 0 {
		return fmt.Errorf("JSON Schema verifier config is required")
	}
	var instance any
	if err := json.Unmarshal(output.Data, &instance); err != nil {
		return fmt.Errorf("decode artifact JSON: %w", err)
	}
	schema, err := compileArtifactSchema(task.ID, task.Verification.Config)
	if err != nil {
		return fmt.Errorf("compile artifact schema: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("artifact does not match JSON Schema: %w", err)
	}
	return nil
}

type TextVerifier struct{}

type TextVerificationConfig struct {
	Contains []string `json:"contains,omitempty"`
	Prefix   string   `json:"prefix,omitempty"`
	Suffix   string   `json:"suffix,omitempty"`
	MinBytes int64    `json:"min_bytes,omitempty"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

func (TextVerifier) Verify(ctx context.Context, task TaskSpec, output Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var config TextVerificationConfig
	if task.Verification != nil && len(task.Verification.Config) > 0 {
		if err := strictjson.DecodeBytes(task.Verification.Config, &config); err != nil {
			return fmt.Errorf("decode text verifier config: %w", err)
		}
	}
	if config.MinBytes < 0 || config.MaxBytes < 0 || (config.MaxBytes > 0 && config.MinBytes > config.MaxBytes) {
		return fmt.Errorf("text verifier byte limits are invalid")
	}
	size := int64(len(output.Data))
	if size < config.MinBytes {
		return fmt.Errorf("artifact is smaller than %d bytes", config.MinBytes)
	}
	if config.MaxBytes > 0 && size > config.MaxBytes {
		return fmt.Errorf("artifact exceeds %d bytes", config.MaxBytes)
	}
	text := string(output.Data)
	if config.Prefix != "" && !strings.HasPrefix(text, config.Prefix) {
		return fmt.Errorf("artifact does not have the required prefix")
	}
	if config.Suffix != "" && !strings.HasSuffix(text, config.Suffix) {
		return fmt.Errorf("artifact does not have the required suffix")
	}
	for _, required := range config.Contains {
		if required == "" || !strings.Contains(text, required) {
			return fmt.Errorf("artifact does not contain required text")
		}
	}
	return nil
}

func compileArtifactSchema(taskID string, raw json.RawMessage) (schema *jsonschema.Schema, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("JSON Schema compiler panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(rejectSchemaLoader{})
	location := "https://schemas.tiggy.local/orchestrator/" + url.PathEscape(taskID)
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

type rejectSchemaLoader struct{}

func (rejectSchemaLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is disabled", location)
}
