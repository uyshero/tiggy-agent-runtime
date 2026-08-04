package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/command"
	"github.com/uyshero/tiggy-agent-runtime/command/local"
)

func TestRunnerReturnsOutputAndNonZeroExit(t *testing.T) {
	t.Parallel()

	runner := newRunner(t, local.Options{RootPath: t.TempDir()})
	result, err := runner.Run(context.Background(), command.Request{
		Script: "printf stdout; printf stderr >&2; exit 7", Directory: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "stdout" || string(result.Stderr) != "stderr" || result.ExitCode != 7 {
		t.Fatalf("Run() = %#v", result)
	}
}

func TestRunnerUsesContainedWorkingDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "work")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, local.Options{RootPath: root})
	result, err := runner.Run(context.Background(), command.Request{Script: "pwd", Directory: "work"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(result.Stdout)) != resolved {
		t.Fatalf("pwd = %q, want %q", result.Stdout, resolved)
	}
}

func TestRunnerRejectsDirectorySymlinkOutsideRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runner := newRunner(t, local.Options{RootPath: root})
	_, err := runner.Run(context.Background(), command.Request{Script: "pwd", Directory: "outside"})
	assertCommandCode(t, err, command.ErrorInvalidDirectory)
}

func TestRunnerEnvironmentIsExplicit(t *testing.T) {
	secretName := "TIGGY_LOCAL_RUNNER_TEST_SECRET"
	t.Setenv(secretName, "host-secret")
	root := t.TempDir()

	defaultRunner := newRunner(t, local.Options{RootPath: root})
	result, err := defaultRunner.Run(context.Background(), command.Request{
		Script: `printf %s "${` + secretName + `-}"`, Directory: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 0 {
		t.Fatalf("default environment inherited host secret: %q", result.Stdout)
	}

	customRunner := newRunner(t, local.Options{
		RootPath: root, Environment: []string{secretName + "=injected"},
	})
	result, err = customRunner.Run(context.Background(), command.Request{
		Script: `printf %s "${` + secretName + `-}"`, Directory: ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "injected" {
		t.Fatalf("custom environment output = %q", result.Stdout)
	}
}

func TestRunnerEnforcesScriptAndCombinedOutputLimits(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	scriptRunner := newRunner(t, local.Options{RootPath: root, MaxScriptBytes: 4})
	_, err := scriptRunner.Run(context.Background(), command.Request{Script: "12345", Directory: "."})
	assertCommandCode(t, err, command.ErrorInvalidRequest)

	outputRunner := newRunner(t, local.Options{RootPath: root, MaxOutputBytes: 4})
	result, err := outputRunner.Run(context.Background(), command.Request{
		Script: "printf 123; printf 456 >&2", Directory: ".",
	})
	assertCommandCode(t, err, command.ErrorOutputLimit)
	if len(result.Stdout)+len(result.Stderr) != 4 {
		t.Fatalf("captured output size = %d, want 4", len(result.Stdout)+len(result.Stderr))
	}
}

func TestRunnerHonorsCancellationAndDeadline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runner := newRunner(t, local.Options{RootPath: root, WaitDelay: 50 * time.Millisecond})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.Run(canceled, command.Request{Script: "touch should-not-exist", Directory: "."})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Run() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "should-not-exist")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled command produced an effect: %v", statErr)
	}

	deadline, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	_, err = runner.Run(deadline, command.Request{Script: "sleep 5", Directory: "."})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline Run() error = %v", err)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	shell := shellPath(t)
	fileRoot := filepath.Join(root, "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nonExecutable := filepath.Join(root, "shell")
	if err := os.WriteFile(nonExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		options local.Options
	}{
		{name: "missing root", options: local.Options{RootPath: filepath.Join(root, "missing"), ShellPath: shell}},
		{name: "file root", options: local.Options{RootPath: fileRoot, ShellPath: shell}},
		{name: "relative shell", options: local.Options{RootPath: root, ShellPath: "bash"}},
		{name: "missing shell", options: local.Options{RootPath: root, ShellPath: filepath.Join(root, "missing")}},
		{name: "directory shell", options: local.Options{RootPath: root, ShellPath: root}},
		{name: "non-executable shell", options: local.Options{RootPath: root, ShellPath: nonExecutable}},
		{name: "invalid environment", options: local.Options{RootPath: root, ShellPath: shell, Environment: []string{"INVALID"}}},
		{name: "duplicate environment", options: local.Options{RootPath: root, ShellPath: shell, Environment: []string{"A=1", "A=2"}}},
		{name: "null environment", options: local.Options{RootPath: root, ShellPath: shell, Environment: []string{"A=x\x00y"}}},
		{name: "negative script limit", options: local.Options{RootPath: root, ShellPath: shell, MaxScriptBytes: -1}},
		{name: "negative output limit", options: local.Options{RootPath: root, ShellPath: shell, MaxOutputBytes: -1}},
		{name: "negative wait delay", options: local.Options{RootPath: root, ShellPath: shell, WaitDelay: -1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := local.New(test.options); err == nil {
				t.Fatal("New() accepted invalid configuration")
			}
		})
	}
}

func TestRunnerOwnsOutputAndSupportsConcurrentCalls(t *testing.T) {
	t.Parallel()

	runner := newRunner(t, local.Options{RootPath: t.TempDir()})
	const calls = 12
	results := make([]command.Result, calls)
	var wait sync.WaitGroup
	wait.Add(calls)
	for index := range results {
		go func(index int) {
			defer wait.Done()
			result, err := runner.Run(context.Background(), command.Request{Script: "printf output", Directory: "."})
			if err != nil {
				t.Errorf("Run() error = %v", err)
				return
			}
			results[index] = result
		}(index)
	}
	wait.Wait()
	for index := range results {
		if string(results[index].Stdout) != "output" {
			t.Fatalf("result %d = %#v", index, results[index])
		}
	}
	results[0].Stdout[0] = 'X'
	if string(results[1].Stdout) != "output" {
		t.Fatal("concurrent results share output storage")
	}
}

func newRunner(t *testing.T, options local.Options) *local.Runner {
	t.Helper()
	if options.ShellPath == "" {
		options.ShellPath = shellPath(t)
	}
	runner, err := local.New(options)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func shellPath(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"/bin/bash", "/bin/sh"} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	t.Skip("no supported absolute shell path is available")
	return ""
}

func assertCommandCode(t *testing.T, err error, code command.ErrorCode) {
	t.Helper()
	var commandError *command.Error
	if !errors.As(err, &commandError) || commandError.Code != code {
		t.Fatalf("error = %v, want command code %q", err, code)
	}
}
