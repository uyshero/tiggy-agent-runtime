package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
	"unicode/utf8"

	"github.com/uyshero/tiggy-agent-runtime/command"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	// BashToolName is the stable model-visible name of BashTool.
	BashToolName = "builtin.bash"
	// DefaultMaxBashScriptBytes bounds model-provided shell source.
	DefaultMaxBashScriptBytes int64 = 32 << 10
	// DefaultMaxBashOutputBytes bounds combined stdout and stderr.
	DefaultMaxBashOutputBytes int64 = 128 << 10
)

// DefaultBashTimeout bounds each command independently of the Agent deadline.
const DefaultBashTimeout = 30 * time.Second

// BashOptions configures a BashTool. A nil Permission denies every call.
type BashOptions struct {
	Runner         command.Runner
	Permission     permission.Policy
	Timeout        time.Duration
	MaxScriptBytes int64
	MaxOutputBytes int64
}

// BashTool executes one bounded shell script through an injected Runner.
type BashTool struct {
	runner         command.Runner
	permission     permission.Policy
	timeout        time.Duration
	maxScriptBytes int64
	maxOutputBytes int64
}

var _ tool.Tool = (*BashTool)(nil)

// NewBash constructs a permission-aware bounded shell Tool.
func NewBash(options BashOptions) (*BashTool, error) {
	if options.Runner == nil {
		return nil, fmt.Errorf("bash tool command runner is required")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultBashTimeout
	}
	maxScript := options.MaxScriptBytes
	if maxScript == 0 {
		maxScript = DefaultMaxBashScriptBytes
	}
	maxOutput := options.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = DefaultMaxBashOutputBytes
	}
	if timeout < 0 || maxScript < 0 || maxOutput < 0 {
		return nil, fmt.Errorf("bash tool limits must be positive")
	}
	return &BashTool{
		runner: options.Runner, permission: options.Permission, timeout: timeout,
		maxScriptBytes: maxScript, maxOutputBytes: maxOutput,
	}, nil
}

func (b *BashTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             BashToolName,
		Description:      "Execute a shell script through the configured bounded command runner.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"script":{"type":"string","minLength":1},
				"path":{"type":"string","maxLength":4096}
			},
			"required":["script"],
			"additionalProperties":false
		}`),
	}
}

func (b *BashTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Script string `json:"script"`
		Path   string `json:"path"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	if input.Script == "" {
		return tool.Result{}, invalidArguments(fmt.Errorf("script is required"))
	}
	if int64(len(input.Script)) > b.maxScriptBytes {
		return tool.Result{}, tool.NewError("command_too_large", "The shell script exceeds the configured size limit.", false, nil)
	}
	directory, err := normalizedCommandDirectory(input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	if err := authorize(ctx, b.permission, BashToolName, permission.ActionExecute, directory); err != nil {
		return tool.Result{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	result, runErr := runCommand(runCtx, b.runner, command.Request{Script: input.Script, Directory: directory})
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return tool.Result{}, tool.NewError("command_timeout", "The command exceeded its execution timeout.", false, runErr)
	}
	if runErr != nil {
		return tool.Result{}, commandError(runErr)
	}
	if err := result.Validate(); err != nil {
		return tool.Result{}, &tool.RuntimeError{Code: "invalid_command_result", Cause: err}
	}
	if int64(len(result.Stdout)+len(result.Stderr)) > b.maxOutputBytes {
		return tool.Result{}, &tool.RuntimeError{Code: "invalid_command_result", Cause: fmt.Errorf("runner returned output over the configured limit")}
	}
	if !utf8.Valid(result.Stdout) || !utf8.Valid(result.Stderr) {
		return tool.Result{}, tool.NewError("unsupported_command_output", "The command output is not valid UTF-8 text.", false, nil)
	}
	summary := fmt.Sprintf("Command exited with code %d.", result.ExitCode)
	if result.ExitCode == -1 {
		summary = "Command terminated without a numeric exit code."
	}
	contents := []message.Content{{Type: message.ContentText, Text: summary}}
	if len(result.Stdout) > 0 {
		contents = append(contents, message.Content{Type: message.ContentText, Text: "stdout:\n" + string(result.Stdout)})
	}
	if len(result.Stderr) > 0 {
		contents = append(contents, message.Content{Type: message.ContentText, Text: "stderr:\n" + string(result.Stderr)})
	}
	return tool.Result{Content: contents}, nil
}

func normalizedCommandDirectory(value string) (string, error) {
	if value == "" {
		return ".", nil
	}
	if len(value) > maximumPathBytes {
		return "", invalidArguments(fmt.Errorf("command path exceeds %d bytes", maximumPathBytes))
	}
	directory, err := safespace.NormalizePath(value)
	if err != nil {
		return "", tool.NewError("invalid_directory", "The command working directory is invalid or outside the configured root.", false, err)
	}
	return directory, nil
}

func runCommand(ctx context.Context, runner command.Runner, request command.Request) (result command.Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &tool.RuntimeError{Code: "command_runner_panic", Cause: fmt.Errorf("panic: %v\n%s", recovered, debug.Stack())}
		}
	}()
	return runner.Run(ctx, request)
}

func commandError(err error) error {
	var commandFailure *command.Error
	if !errors.As(err, &commandFailure) || commandFailure == nil {
		var runtimeFailure *tool.RuntimeError
		if errors.As(err, &runtimeFailure) && runtimeFailure != nil {
			return runtimeFailure
		}
		return &tool.RuntimeError{Code: "command_runner_failed", Cause: err}
	}
	switch commandFailure.Code {
	case command.ErrorInvalidDirectory:
		return tool.NewError("invalid_directory", "The command working directory is unavailable.", false, err)
	case command.ErrorOutputLimit:
		return tool.NewError("command_output_limit", "The command exceeded its output limit.", false, err)
	default:
		return &tool.RuntimeError{Code: string(commandFailure.Code), Cause: err}
	}
}
