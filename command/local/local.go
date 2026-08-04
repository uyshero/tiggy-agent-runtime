// Package local provides an explicit opt-in os/exec Command Runner.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/command"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
)

const (
	defaultMaxScriptBytes = 32 << 10
	defaultMaxOutputBytes = 128 << 10
	defaultWaitDelay      = 2 * time.Second
)

// Options configures a local shell process. RootPath and an absolute ShellPath
// are required. A nil Environment uses a small non-secret default; a non-nil
// slice is copied and used exactly.
type Options struct {
	RootPath       string
	ShellPath      string
	Environment    []string
	MaxScriptBytes int64
	MaxOutputBytes int64
	WaitDelay      time.Duration
}

// Runner starts one local shell process per request. RootPath constrains only
// the initial working directory; Runner is not a filesystem or process sandbox.
type Runner struct {
	rootPath       string
	shellPath      string
	environment    []string
	maxScriptBytes int64
	maxOutputBytes int64
	waitDelay      time.Duration
}

var _ command.Runner = (*Runner)(nil)

// New validates and copies local process configuration.
func New(options Options) (*Runner, error) {
	root, err := canonicalDirectory(options.RootPath)
	if err != nil {
		return nil, fmt.Errorf("command root: %w", err)
	}
	shell := strings.TrimSpace(options.ShellPath)
	if shell == "" || !filepath.IsAbs(shell) {
		return nil, fmt.Errorf("command shell path must be absolute")
	}
	shell, err = filepath.EvalSymlinks(shell)
	if err != nil {
		return nil, fmt.Errorf("resolve command shell: %w", err)
	}
	info, err := os.Stat(shell)
	if err != nil {
		return nil, fmt.Errorf("stat command shell: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("command shell must be an executable regular file")
	}
	maxScript := options.MaxScriptBytes
	if maxScript == 0 {
		maxScript = defaultMaxScriptBytes
	}
	maxOutput := options.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = defaultMaxOutputBytes
	}
	waitDelay := options.WaitDelay
	if waitDelay == 0 {
		waitDelay = defaultWaitDelay
	}
	if maxScript < 0 || maxOutput < 0 || waitDelay < 0 {
		return nil, fmt.Errorf("command limits must be positive")
	}
	environment := options.Environment
	if environment == nil {
		environment = []string{
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"HOME=" + root,
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
		}
	}
	environment, err = validatedEnvironment(environment)
	if err != nil {
		return nil, err
	}
	return &Runner{
		rootPath: root, shellPath: shell, environment: environment,
		maxScriptBytes: maxScript, maxOutputBytes: maxOutput, waitDelay: waitDelay,
	}, nil
}

// Run executes the configured shell with `-c`. Non-zero exits are returned as
// successful Results so the model can inspect stdout, stderr, and ExitCode.
func (r *Runner) Run(ctx context.Context, request command.Request) (command.Result, error) {
	if ctx == nil {
		return command.Result{}, &command.Error{Code: command.ErrorInvalidRequest, Cause: fmt.Errorf("context is nil")}
	}
	if err := ctx.Err(); err != nil {
		return command.Result{}, err
	}
	if err := request.Validate(); err != nil {
		return command.Result{}, &command.Error{Code: command.ErrorInvalidRequest, Cause: err}
	}
	if int64(len(request.Script)) > r.maxScriptBytes {
		return command.Result{}, &command.Error{Code: command.ErrorInvalidRequest, Cause: fmt.Errorf("script exceeds configured limit")}
	}
	directory, err := r.resolveDirectory(request.Directory)
	if err != nil {
		return command.Result{}, &command.Error{Code: command.ErrorInvalidDirectory, Cause: err}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	capture := newCapture(r.maxOutputBytes, cancel)
	process := exec.CommandContext(runCtx, r.shellPath, "-c", request.Script)
	process.Dir = directory
	process.Env = append([]string(nil), r.environment...)
	process.Stdout = capture.stdoutWriter()
	process.Stderr = capture.stderrWriter()
	process.WaitDelay = r.waitDelay
	runErr := process.Run()
	result := capture.result()
	result.ExitCode = 0
	if process.ProcessState != nil {
		result.ExitCode = process.ProcessState.ExitCode()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if capture.limitExceeded() {
		return result, &command.Error{Code: command.ErrorOutputLimit, Cause: fmt.Errorf("combined command output exceeded %d bytes", r.maxOutputBytes)}
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		return result, nil
	}
	if runErr != nil {
		code := command.ErrorStartFailed
		if errors.Is(runErr, exec.ErrWaitDelay) {
			code = command.ErrorRunnerFailed
		}
		return result, &command.Error{Code: code, Cause: runErr}
	}
	return result, nil
}

func (r *Runner) resolveDirectory(directory string) (string, error) {
	normalized, err := safespace.NormalizePath(directory)
	if err != nil || normalized != directory {
		if err == nil {
			err = fmt.Errorf("directory is not canonical")
		}
		return "", err
	}
	candidate := filepath.Join(r.rootPath, filepath.FromSlash(normalized))
	resolved, err := canonicalDirectory(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(r.rootPath, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("directory resolves outside command root")
	}
	return resolved, nil
}

func canonicalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("directory is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return resolved, nil
}

func validatedEnvironment(values []string) ([]string, error) {
	result := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("command environment contains a null byte")
		}
		name, _, found := strings.Cut(value, "=")
		if !found || name == "" || strings.ContainsRune(name, '=') {
			return nil, fmt.Errorf("command environment entry %q is invalid", value)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("command environment contains duplicate variable %q", name)
		}
		seen[name] = struct{}{}
		result[index] = value
	}
	return result, nil
}

type capture struct {
	mu        sync.Mutex
	remaining int64
	exceeded  bool
	cancel    context.CancelFunc
	stdout    bytes.Buffer
	stderr    bytes.Buffer
}

func newCapture(maximum int64, cancel context.CancelFunc) *capture {
	return &capture{remaining: maximum, cancel: cancel}
}

func (c *capture) stdoutWriter() captureWriter {
	return captureWriter{capture: c, destination: &c.stdout}
}

func (c *capture) stderrWriter() captureWriter {
	return captureWriter{capture: c, destination: &c.stderr}
}

func (c *capture) result() command.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return command.Result{
		Stdout: append([]byte(nil), c.stdout.Bytes()...),
		Stderr: append([]byte(nil), c.stderr.Bytes()...),
	}
}

func (c *capture) limitExceeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exceeded
}

type captureWriter struct {
	capture     *capture
	destination *bytes.Buffer
}

func (w captureWriter) Write(value []byte) (int, error) {
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()
	accepted := int64(len(value))
	if accepted > w.capture.remaining {
		accepted = w.capture.remaining
	}
	if accepted > 0 {
		_, _ = w.destination.Write(value[:accepted])
		w.capture.remaining -= accepted
	}
	if accepted < int64(len(value)) {
		w.capture.exceeded = true
		w.capture.cancel()
	}
	return len(value), nil
}
