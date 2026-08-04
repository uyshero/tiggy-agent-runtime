package toolkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime/debug"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// compileInputSchema 按 Draft 2020-12 编译 Input Schema。
// 开启 Format Assertion；禁用外部 $ref 加载，避免编译期网络/文件 I/O。
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

// schemaPanicError 包装校验期 panic，避免冲垮宿主进程。
type schemaPanicError struct {
	cause error
}

func (e *schemaPanicError) Error() string {
	return e.cause.Error()
}

func (e *schemaPanicError) Unwrap() error {
	return e.cause
}

// validateInput 对已解码参数实例执行 Schema 校验，并隔离 panic。
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

// offlineSchemaLoader 拒绝一切外部 Schema 资源加载。
type offlineSchemaLoader struct{}

func (offlineSchemaLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is disabled", location)
}
