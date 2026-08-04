package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMaxWireMessageBytes = 4 << 20
	defaultTerminateDuration   = 5 * time.Second
)

// StdioOptions configures an MCP server subprocess. Path and Directory must
// be absolute. A nil Environment is an empty environment, not host inheritance.
type StdioOptions struct {
	Implementation    Implementation
	Path              string
	Args              []string
	Directory         string
	Environment       []string
	Stderr            io.Writer
	MaxMessageBytes   int64
	TerminateDuration time.Duration
}

// NewStdioConnector validates reusable subprocess configuration. Each Connect
// starts a fresh process without invoking a shell.
func NewStdioConnector(options StdioOptions) (Connector, error) {
	if err := options.Implementation.validate(); err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	path, err := executablePath(options.Path)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	directory, err := existingDirectory(options.Directory)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	args := append([]string(nil), options.Args...)
	for _, value := range args {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("MCP process argument contains a null byte")}
		}
	}
	environment, err := validateEnvironment(options.Environment)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	maximum := options.MaxMessageBytes
	if maximum == 0 {
		maximum = defaultMaxWireMessageBytes
	}
	terminate := options.TerminateDuration
	if terminate == 0 {
		terminate = defaultTerminateDuration
	}
	if maximum < 0 || terminate < 0 {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("MCP stdio limits must be positive")}
	}
	implementation := options.Implementation
	stderr := options.Stderr
	return newSDKConnector(implementation, func() (mcpsdk.Transport, error) {
		return &processTransport{
			path: path, args: append([]string(nil), args...), directory: directory,
			environment: copyEnvironment(environment), stderr: stderr,
			maxMessageBytes: maximum, terminateDuration: terminate,
		}, nil
	})
}

func copyEnvironment(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func executablePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("MCP executable path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve MCP executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat MCP executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("MCP executable must be an executable regular file")
	}
	return resolved, nil
}

func existingDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("MCP process directory must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("resolve MCP process directory: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat MCP process directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("MCP process directory is not a directory")
	}
	return resolved, nil
}

func validateEnvironment(values []string) ([]string, error) {
	result := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("MCP process environment contains a null byte")
		}
		name, _, found := strings.Cut(value, "=")
		if !found || name == "" {
			return nil, fmt.Errorf("MCP process environment entry is invalid")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("MCP process environment contains duplicate variable %q", name)
		}
		seen[name] = struct{}{}
		result[index] = value
	}
	return result, nil
}

type processTransport struct {
	path              string
	args              []string
	directory         string
	environment       []string
	stderr            io.Writer
	maxMessageBytes   int64
	terminateDuration time.Duration
}

func (t *processTransport) Connect(ctx context.Context) (mcpsdk.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(t.path, t.args...)
	command.Dir = t.directory
	command.Env = copyEnvironment(t.environment)
	command.Stderr = t.stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	reader := &boundedLineReadCloser{reader: stdout, maximum: t.maxMessageBytes}
	connection, err := (&mcpsdk.IOTransport{Reader: reader, Writer: stdin}).Connect(ctx)
	if err != nil {
		_ = reader.Close()
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	return &processConnection{
		Connection: connection, process: command.Process, wait: wait,
		terminateDuration: t.terminateDuration,
	}, nil
}

type processConnection struct {
	mcpsdk.Connection
	process           *os.Process
	wait              <-chan error
	terminateDuration time.Duration
	closeOnce         sync.Once
	closeErr          error
}

func (c *processConnection) Close() error {
	c.closeOnce.Do(func() {
		connectionErr := c.Connection.Close()
		processErr := c.awaitProcess()
		c.closeErr = errors.Join(connectionErr, processErr)
	})
	return c.closeErr
}

func (c *processConnection) awaitProcess() error {
	if err, completed := waitProcess(c.wait, c.terminateDuration); completed {
		return err
	}
	_ = c.process.Signal(os.Interrupt)
	if err, completed := waitProcess(c.wait, c.terminateDuration); completed {
		return processExitError(err)
	}
	if err := c.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	if err, completed := waitProcess(c.wait, c.terminateDuration); completed {
		return processExitError(err)
	}
	return fmt.Errorf("MCP subprocess did not terminate")
}

func waitProcess(wait <-chan error, duration time.Duration) (error, bool) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case err := <-wait:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

func processExitError(err error) error {
	var exitError *exec.ExitError
	if err == nil || errors.As(err, &exitError) {
		return nil
	}
	return err
}

type boundedLineReadCloser struct {
	reader  io.ReadCloser
	maximum int64
	current int64
	pending error
}

func (r *boundedLineReadCloser) Read(value []byte) (int, error) {
	if r.pending != nil {
		return 0, r.pending
	}
	count, err := r.reader.Read(value)
	for index, current := range value[:count] {
		if current == '\n' {
			r.current = 0
			continue
		}
		r.current++
		if r.current > r.maximum {
			r.pending = fmt.Errorf("MCP wire message exceeded %d bytes", r.maximum)
			if index == 0 {
				return 0, r.pending
			}
			return index, nil
		}
	}
	return count, err
}

func (r *boundedLineReadCloser) Close() error {
	return r.reader.Close()
}
