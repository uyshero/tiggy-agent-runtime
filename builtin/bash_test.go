package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/builtin"
	"github.com/uyshero/tiggy-agent-runtime/command"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

func TestBashUsesCanonicalExecutePermissionWithoutScript(t *testing.T) {
	t.Parallel()

	const script = "printf super-secret-script"
	var permissionRequest permission.Request
	var runnerRequest command.Request
	bash := newBash(t, builtin.BashOptions{
		Permission: permission.PolicyFunc(func(_ context.Context, request permission.Request) (permission.Decision, error) {
			permissionRequest = request
			return permission.DecisionAllow, nil
		}),
		Runner: command.RunnerFunc(func(_ context.Context, request command.Request) (command.Result, error) {
			runnerRequest = request
			return command.Result{ExitCode: 0}, nil
		}),
	})
	_, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"printf super-secret-script","path":"work/../actual"}`))
	if err != nil {
		t.Fatal(err)
	}
	if permissionRequest.Tool != builtin.BashToolName || permissionRequest.Action != permission.ActionExecute || permissionRequest.Target != "actual" {
		t.Fatalf("permission request = %#v", permissionRequest)
	}
	if strings.Contains(fmt.Sprintf("%#v", permissionRequest), script) {
		t.Fatalf("permission request leaked script: %#v", permissionRequest)
	}
	if runnerRequest.Script != script || runnerRequest.Directory != "actual" {
		t.Fatalf("runner request = %#v", runnerRequest)
	}
}

func TestBashDefaultDenyAndValidationOccurBeforeRunner(t *testing.T) {
	t.Parallel()

	runnerCalls := 0
	runner := command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
		runnerCalls++
		return command.Result{}, nil
	})
	bash := newBash(t, builtin.BashOptions{Runner: runner})
	_, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"true"}`))
	assertToolCode(t, err, "permission_denied")
	if runnerCalls != 0 {
		t.Fatal("denied Bash call reached Runner")
	}

	policyCalls := 0
	bash = newBash(t, builtin.BashOptions{
		Runner: runner, MaxScriptBytes: 4,
		Permission: permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
			policyCalls++
			return permission.DecisionAllow, nil
		}),
	})
	for _, test := range []struct {
		arguments json.RawMessage
		code      string
	}{
		{arguments: json.RawMessage(`{"script":""}`), code: "invalid_tool_arguments"},
		{arguments: json.RawMessage(`{"script":"12345"}`), code: "command_too_large"},
		{arguments: json.RawMessage(`{"script":"true","path":"../outside"}`), code: "invalid_directory"},
	} {
		_, err := bash.Execute(context.Background(), test.arguments)
		assertToolCode(t, err, test.code)
	}
	if policyCalls != 0 || runnerCalls != 0 {
		t.Fatalf("invalid calls reached dependencies: policy %d, runner %d", policyCalls, runnerCalls)
	}
}

func TestBashCancellationAndTimeout(t *testing.T) {
	t.Parallel()

	runner := command.RunnerFunc(func(ctx context.Context, _ command.Request) (command.Result, error) {
		<-ctx.Done()
		return command.Result{}, ctx.Err()
	})
	bash := newBash(t, builtin.BashOptions{
		Runner: runner, Permission: permission.AllowAll{}, Timeout: 20 * time.Millisecond,
	})
	_, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"wait"}`))
	assertToolCode(t, err, "command_timeout")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = bash.Execute(ctx, json.RawMessage(`{"script":"wait"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("external cancellation error = %v", err)
	}
}

func TestBashNonZeroExitIsAValidResult(t *testing.T) {
	t.Parallel()

	bash := newBash(t, builtin.BashOptions{
		Permission: permission.AllowAll{},
		Runner: command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
			return command.Result{Stdout: []byte("out"), Stderr: []byte("err"), ExitCode: 9}, nil
		}),
	})
	result, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"exit 9"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 3 || result.Content[0].Text != "Command exited with code 9." || result.Content[1].Text != "stdout:\nout" || result.Content[2].Text != "stderr:\nerr" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBashClassifiesRunnerFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		err         error
		toolCode    string
		runtimeCode string
	}{
		{name: "directory", err: &command.Error{Code: command.ErrorInvalidDirectory}, toolCode: "invalid_directory"},
		{name: "output", err: &command.Error{Code: command.ErrorOutputLimit}, toolCode: "command_output_limit"},
		{name: "invalid request", err: &command.Error{Code: command.ErrorInvalidRequest}, runtimeCode: "invalid_command_request"},
		{name: "start", err: &command.Error{Code: command.ErrorStartFailed}, runtimeCode: "command_start_failed"},
		{name: "runner", err: &command.Error{Code: command.ErrorRunnerFailed}, runtimeCode: "command_runner_failed"},
		{name: "unknown", err: errors.New("transport failed"), runtimeCode: "command_runner_failed"},
		{name: "runtime preserved", err: &tool.RuntimeError{Code: "adapter_failed", Cause: errors.New("failed")}, runtimeCode: "adapter_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bash := newBash(t, builtin.BashOptions{
				Permission: permission.AllowAll{},
				Runner: command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
					return command.Result{}, test.err
				}),
			})
			_, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"true"}`))
			if test.toolCode != "" {
				assertToolCode(t, err, test.toolCode)
			} else {
				assertRuntimeCode(t, err, test.runtimeCode)
			}
		})
	}
}

func TestBashContainsRunnerPanic(t *testing.T) {
	t.Parallel()

	bash := newBash(t, builtin.BashOptions{
		Permission: permission.AllowAll{},
		Runner: command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
			panic("boom")
		}),
	})
	_, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"true"}`))
	assertRuntimeCode(t, err, "command_runner_panic")
}

func TestBashRejectsInvalidRunnerResults(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		result      command.Result
		toolCode    string
		runtimeCode string
	}{
		{name: "oversized", result: command.Result{Stdout: []byte("12345")}, runtimeCode: "invalid_command_result"},
		{name: "invalid utf8", result: command.Result{Stdout: []byte{0xff}}, toolCode: "unsupported_command_output"},
		{name: "invalid exit code", result: command.Result{ExitCode: -2}, runtimeCode: "invalid_command_result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bash := newBash(t, builtin.BashOptions{
				Permission: permission.AllowAll{}, MaxOutputBytes: 4,
				Runner: command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
					return test.result, nil
				}),
			})
			_, err := bash.Execute(context.Background(), json.RawMessage(`{"script":"true"}`))
			if test.toolCode != "" {
				assertToolCode(t, err, test.toolCode)
			} else {
				assertRuntimeCode(t, err, test.runtimeCode)
			}
		})
	}
}

func TestNewBashRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	validRunner := command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
		return command.Result{}, nil
	})
	for _, options := range []builtin.BashOptions{
		{},
		{Runner: validRunner, Timeout: -1},
		{Runner: validRunner, MaxScriptBytes: -1},
		{Runner: validRunner, MaxOutputBytes: -1},
	} {
		if _, err := builtin.NewBash(options); err == nil {
			t.Fatalf("NewBash(%#v) accepted invalid options", options)
		}
	}
}

func newBash(t *testing.T, options builtin.BashOptions) *builtin.BashTool {
	t.Helper()
	value, err := builtin.NewBash(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
