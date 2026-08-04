package toolkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime/debug"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func compileInputSchema(name string, raw json.RawMessage) (validator argumentValidator, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("JSON Schema compiler panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	document, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(offlineSchemaLoader{})
	location := "https://schemas.tiggy.local/tools/" + url.PathEscape(name) + "/input"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

type schemaPanicError struct {
	cause error
}

func (e *schemaPanicError) Error() string {
	return e.cause.Error()
}

func (e *schemaPanicError) Unwrap() error {
	return e.cause
}

func validateInput(validator argumentValidator, instance any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &schemaPanicError{cause: fmt.Errorf("JSON Schema validator panic: %v\n%s", recovered, debug.Stack())}
		}
	}()
	return validator.Validate(instance)
}

func decodeJSON(raw json.RawMessage) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

type offlineSchemaLoader struct{}

func (offlineSchemaLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is disabled", location)
}
