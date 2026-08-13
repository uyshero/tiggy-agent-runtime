package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/builtin"
	"github.com/uyshero/tiggy-agent-runtime/command"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestReadWriteUseSafeSpaceAndCanonicalPermissionTarget(t *testing.T) {
	t.Parallel()

	space, err := safespace.OpenSafeSpace(t.TempDir(), safespace.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = space.Close() })
	var requests []permission.Request
	policy := permission.PolicyFunc(func(_ context.Context, request permission.Request) (permission.Decision, error) {
		requests = append(requests, request)
		return permission.DecisionAllow, nil
	})
	read, err := builtin.NewRead(builtin.ReadOptions{FileSystem: space, Permission: policy})
	if err != nil {
		t.Fatal(err)
	}
	write, err := builtin.NewWrite(builtin.WriteOptions{FileSystem: space, Permission: policy})
	if err != nil {
		t.Fatal(err)
	}
	kit, err := toolkit.New(read, write)
	if err != nil {
		t.Fatal(err)
	}
	view := kit.Snapshot()
	writeResult, err := view.Call(context.Background(), message.ToolCall{
		ID: "call_write", Name: builtin.WriteToolName,
		Arguments: json.RawMessage(`{"path":"notes/../today.txt","content":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writeResult.Content) != 1 || writeResult.Content[0].Text != `Wrote 5 bytes to "today.txt".` {
		t.Fatalf("write result = %#v", writeResult)
	}
	readResult, err := view.Call(context.Background(), message.ToolCall{
		ID: "call_read", Name: builtin.ReadToolName, Arguments: json.RawMessage(`{"path":"today.txt"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readResult.Content) != 2 || readResult.Content[1].Text != "hello" || !strings.Contains(readResult.Content[0].Text, "file_digest=sha256:") {
		t.Fatalf("read result = %#v", readResult)
	}
	var state struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(readResult.State, &state); err != nil || state.Path != "today.txt" || !strings.HasPrefix(state.Digest, "sha256:") {
		t.Fatalf("read state = %s, %v", readResult.State, err)
	}
	if len(requests) != 2 || requests[0].Tool != builtin.WriteToolName || requests[0].Action != permission.ActionWrite || requests[0].Target != "today.txt" || requests[1].Action != permission.ActionRead {
		t.Fatalf("permission requests = %#v", requests)
	}
}

func TestReadReturnsValidResultForEmptyFile(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{readData: []byte{}}
	read := newRead(t, builtin.ReadOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	kit, err := toolkit.New(read)
	if err != nil {
		t.Fatal(err)
	}
	result, err := kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_empty", Name: builtin.ReadToolName, Arguments: json.RawMessage(`{"path":"empty.txt"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, `Read 0 bytes from "empty.txt"; the file is empty.`) || !strings.Contains(result.Content[0].Text, "file_digest=sha256:") {
		t.Fatalf("result = %#v", result)
	}
	var state struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(result.State, &state); err != nil || state.Path != "empty.txt" || !strings.HasPrefix(state.Digest, "sha256:") {
		t.Fatalf("state = %s, %v", result.State, err)
	}
}

func TestFileToolsDefaultDenyBeforeIO(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{}
	read := newRead(t, builtin.ReadOptions{FileSystem: fileSystem})
	_, err := read.Execute(context.Background(), json.RawMessage(`{"path":"file.txt"}`))
	assertToolCode(t, err, "permission_denied")
	write := newWrite(t, builtin.WriteOptions{FileSystem: fileSystem})
	_, err = write.Execute(context.Background(), json.RawMessage(`{"path":"file.txt","content":"value"}`))
	assertToolCode(t, err, "permission_denied")
	if fileSystem.readCalls != 0 || fileSystem.writeCalls != 0 {
		t.Fatalf("filesystem calls = read %d, write %d", fileSystem.readCalls, fileSystem.writeCalls)
	}
}

func TestFileToolsValidateBeforePermission(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{}
	policyCalls := 0
	policy := permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
		policyCalls++
		return permission.DecisionAllow, nil
	})
	read := newRead(t, builtin.ReadOptions{FileSystem: fileSystem, Permission: policy})
	for _, test := range []struct {
		arguments json.RawMessage
		code      string
	}{
		{arguments: json.RawMessage(`{}`), code: "invalid_tool_arguments"},
		{arguments: json.RawMessage(`{"path":"../outside"}`), code: "invalid_path"},
		{arguments: json.RawMessage(`{"path":"a\u0000b"}`), code: "invalid_path"},
		{arguments: json.RawMessage(`{"path":"file.txt","extra":true}`), code: "invalid_tool_arguments"},
		{arguments: json.RawMessage(`{"path":"first.txt","path":"second.txt"}`), code: "invalid_tool_arguments"},
	} {
		_, err := read.Execute(context.Background(), test.arguments)
		assertToolCode(t, err, test.code)
	}
	if policyCalls != 0 || fileSystem.readCalls != 0 {
		t.Fatalf("validation triggered policy or IO: policy %d, read %d", policyCalls, fileSystem.readCalls)
	}
}

func TestReadEnforcesOutputLimitAndUTF8(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		data []byte
		max  int64
		code string
	}{
		{name: "too large", data: []byte("12345"), max: 4, code: "file_too_large"},
		{name: "binary", data: []byte{0xff, 0xfe}, max: 4, code: "unsupported_file_encoding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := newRead(t, builtin.ReadOptions{
				FileSystem: &fakeFileSystem{readData: test.data}, Permission: permission.AllowAll{}, MaxBytes: test.max,
			})
			_, err := read.Execute(context.Background(), json.RawMessage(`{"path":"file.txt"}`))
			assertToolCode(t, err, test.code)
		})
	}
}

func TestWriteEnforcesInputLimitBeforePermissionAndIO(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{}
	policyCalls := 0
	write := newWrite(t, builtin.WriteOptions{
		FileSystem: fileSystem,
		Permission: permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
			policyCalls++
			return permission.DecisionAllow, nil
		}),
		MaxBytes: 4,
	})
	_, err := write.Execute(context.Background(), json.RawMessage(`{"path":"file.txt","content":"12345"}`))
	assertToolCode(t, err, "file_too_large")
	if policyCalls != 0 || fileSystem.writeCalls != 0 {
		t.Fatalf("oversized write triggered policy or IO: policy %d, write %d", policyCalls, fileSystem.writeCalls)
	}
}

func TestFileSystemErrorsAreClassified(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		err         error
		toolCode    string
		runtimeCode string
	}{
		{name: "not found", err: fs.ErrNotExist, toolCode: "file_not_found"},
		{name: "os permission", err: fs.ErrPermission, toolCode: "file_access_denied"},
		{name: "not regular", err: &safespace.Error{Code: safespace.ErrorNotRegular}, toolCode: "not_regular_file"},
		{name: "safespace limit", err: &safespace.Error{Code: safespace.ErrorReadLimit}, toolCode: "file_too_large"},
		{name: "unknown IO", err: errors.New("device failed"), runtimeCode: "file_system_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := newRead(t, builtin.ReadOptions{
				FileSystem: &fakeFileSystem{readErr: test.err}, Permission: permission.AllowAll{},
			})
			_, err := read.Execute(context.Background(), json.RawMessage(`{"path":"file.txt"}`))
			if test.toolCode != "" {
				assertToolCode(t, err, test.toolCode)
			} else {
				assertRuntimeCode(t, err, test.runtimeCode)
			}
		})
	}
}

func TestPermissionFailureIsPreservedAsRuntimeError(t *testing.T) {
	t.Parallel()

	read := newRead(t, builtin.ReadOptions{
		FileSystem: &fakeFileSystem{},
		Permission: permission.PolicyFunc(func(context.Context, permission.Request) (permission.Decision, error) {
			panic("policy boom")
		}),
	})
	kit, err := toolkit.New(read)
	if err != nil {
		t.Fatal(err)
	}
	_, err = kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_read", Name: builtin.ReadToolName, Arguments: json.RawMessage(`{"path":"file.txt"}`),
	})
	assertRuntimeCode(t, err, "permission_policy_failed")
}

func TestFileToolsPropagateCancellationBeforeIO(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fileSystem := &fakeFileSystem{}
	read := newRead(t, builtin.ReadOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err := read.Execute(ctx, json.RawMessage(`{"path":"file.txt"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read Execute() error = %v", err)
	}
	write := newWrite(t, builtin.WriteOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err = write.Execute(ctx, json.RawMessage(`{"path":"file.txt","content":"value"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write Execute() error = %v", err)
	}
	if fileSystem.readCalls != 0 || fileSystem.writeCalls != 0 {
		t.Fatal("canceled tools performed IO")
	}
}

func TestFileToolsObserveCancellationAfterIOReturns(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	readFileSystem := &fakeFileSystem{readData: []byte("ignored"), readHook: cancel}
	read := newRead(t, builtin.ReadOptions{FileSystem: readFileSystem, Permission: permission.AllowAll{}})
	_, err := read.Execute(ctx, json.RawMessage(`{"path":"file.txt"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read Execute() error = %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	writeFileSystem := &fakeFileSystem{writeHook: cancel}
	write := newWrite(t, builtin.WriteOptions{FileSystem: writeFileSystem, Permission: permission.AllowAll{}})
	_, err = write.Execute(ctx, json.RawMessage(`{"path":"file.txt","content":"value"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write Execute() error = %v", err)
	}
}

func TestWritePassesCanonicalPathContentAndMode(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{}
	write := newWrite(t, builtin.WriteOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	result, err := write.Execute(context.Background(), json.RawMessage(`{"path":"notes/../file.txt","content":"value"}`))
	if err != nil {
		t.Fatal(err)
	}
	if fileSystem.writePath != "file.txt" || string(fileSystem.writeData) != "value" || fileSystem.writeMode != 0o644 {
		t.Fatalf("WriteFile() = path %q, data %q, mode %o", fileSystem.writePath, fileSystem.writeData, fileSystem.writeMode)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `Wrote 5 bytes to "file.txt".` {
		t.Fatalf("result = %#v", result)
	}
}

func TestConstructorsAndDefinitions(t *testing.T) {
	t.Parallel()

	if _, err := builtin.NewRead(builtin.ReadOptions{}); err == nil {
		t.Fatal("NewRead() accepted a nil filesystem")
	}
	if _, err := builtin.NewWrite(builtin.WriteOptions{}); err == nil {
		t.Fatal("NewWrite() accepted a nil filesystem")
	}
	fileSystem := &fakeFileSystem{}
	if _, err := builtin.NewRead(builtin.ReadOptions{FileSystem: fileSystem, MaxBytes: -1}); err == nil {
		t.Fatal("NewRead() accepted a negative limit")
	}
	if _, err := builtin.NewWrite(builtin.WriteOptions{FileSystem: fileSystem, MaxBytes: -1}); err == nil {
		t.Fatal("NewWrite() accepted a negative limit")
	}
	read := newRead(t, builtin.ReadOptions{FileSystem: fileSystem})
	write := newWrite(t, builtin.WriteOptions{FileSystem: fileSystem})
	edit := newEdit(t, builtin.EditOptions{FileSystem: fileSystem})
	todo := newTodo(t, builtin.TodoOptions{})
	glob := newGlob(t, builtin.GlobOptions{FileSystem: fileSystem})
	grep := newGrep(t, builtin.GrepOptions{FileSystem: fileSystem})
	bash := newBash(t, builtin.BashOptions{
		Runner: command.RunnerFunc(func(context.Context, command.Request) (command.Result, error) {
			return command.Result{}, nil
		}),
	})
	for _, definition := range []model.ToolDefinition{
		read.Definition(), write.Definition(), edit.Definition(), todo.Definition(), glob.Definition(), grep.Definition(), bash.Definition(),
	} {
		if err := definition.Validate(); err != nil {
			t.Fatalf("Definition(%q) error = %v", definition.Name, err)
		}
		if definition.ConcurrencyClass != model.ToolConcurrencySequential {
			t.Fatalf("Definition(%q) concurrency = %q", definition.Name, definition.ConcurrencyClass)
		}
	}
	if _, err := toolkit.New(read, write, edit, glob, grep, bash); err != nil {
		t.Fatalf("Toolkit registration error = %v", err)
	}
	if _, err := toolkit.New(read, write, edit, todo, glob, grep, bash); err != nil {
		t.Fatalf("Toolkit registration with todo error = %v", err)
	}
}

func newRead(t *testing.T, options builtin.ReadOptions) *builtin.ReadTool {
	t.Helper()
	value, err := builtin.NewRead(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newWrite(t *testing.T, options builtin.WriteOptions) *builtin.WriteTool {
	t.Helper()
	value, err := builtin.NewWrite(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertToolCode(t *testing.T, err error, code string) {
	t.Helper()
	var toolError *tool.Error
	if !errors.As(err, &toolError) || toolError.Code != code {
		t.Fatalf("error = %v, want tool code %q", err, code)
	}
}

func assertRuntimeCode(t *testing.T, err error, code string) {
	t.Helper()
	var runtimeError *tool.RuntimeError
	if !errors.As(err, &runtimeError) || runtimeError.Code != code {
		t.Fatalf("error = %v, want runtime code %q", err, code)
	}
}

type fakeFileSystem struct {
	readData    []byte
	readErr     error
	writeErr    error
	readCalls   int
	writeCalls  int
	readPath    string
	writePath   string
	writeData   []byte
	writeMode   fs.FileMode
	readHook    func()
	writeHook   func()
	listEntries []safespace.Entry
	listErr     error
	listCalls   int
	listHook    func()
}

func (f *fakeFileSystem) ReadFile(ctx context.Context, path string) ([]byte, error) {
	f.readCalls++
	f.readPath = path
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.readHook != nil {
		f.readHook()
	}
	return append([]byte(nil), f.readData...), f.readErr
}

func (f *fakeFileSystem) WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	f.writeCalls++
	f.writePath = path
	f.writeData = append([]byte(nil), data...)
	f.writeMode = mode
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.writeHook != nil {
		f.writeHook()
	}
	return f.writeErr
}

func (f *fakeFileSystem) List(ctx context.Context, _ string) ([]safespace.Entry, error) {
	f.listCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.listHook != nil {
		f.listHook()
	}
	return append([]safespace.Entry(nil), f.listEntries...), f.listErr
}

var _ safespace.FileSystem = (*fakeFileSystem)(nil)
