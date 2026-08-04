package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/builtin"
	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
)

func TestGlobMatchesDoublestarPathsDeterministically(t *testing.T) {
	t.Parallel()

	space := populatedSearchSpace(t)
	var request permission.Request
	glob := newGlob(t, builtin.GlobOptions{
		FileSystem: space,
		Permission: permission.PolicyFunc(func(_ context.Context, value permission.Request) (permission.Decision, error) {
			request = value
			return permission.DecisionAllow, nil
		}),
	})
	result, err := glob.Execute(context.Background(), json.RawMessage(`{"path":"src","pattern":"**/*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Tool != builtin.GlobToolName || request.Action != permission.ActionSearch || request.Target != "src" {
		t.Fatalf("permission request = %#v", request)
	}
	if len(result.Content) != 2 || result.Content[0].Text != "Matched 4 path(s)." {
		t.Fatalf("result = %#v", result)
	}
	expected := "src/binary.go\nsrc/large.go\nsrc/main.go\nsrc/nested/test.go"
	if result.Content[1].Text != expected {
		t.Fatalf("matches = %q, want %q", result.Content[1].Text, expected)
	}
}

func TestGlobBoundsResultsAndTraversal(t *testing.T) {
	t.Parallel()

	space := populatedSearchSpace(t)
	t.Run("result truncation", func(t *testing.T) {
		glob := newGlob(t, builtin.GlobOptions{
			FileSystem: space, Permission: permission.AllowAll{}, Limits: builtin.SearchLimits{MaxResults: 1},
		})
		result, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.go"}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 2 || !strings.Contains(result.Content[0].Text, "truncated") || strings.Contains(result.Content[1].Text, "\n") {
			t.Fatalf("truncated result = %#v", result)
		}
	})

	t.Run("entry limit", func(t *testing.T) {
		glob := newGlob(t, builtin.GlobOptions{
			FileSystem: space, Permission: permission.AllowAll{}, Limits: builtin.SearchLimits{MaxEntries: 1},
		})
		_, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":"**"}`))
		assertToolCode(t, err, "search_limit_exceeded")
	})

	t.Run("output limit", func(t *testing.T) {
		glob := newGlob(t, builtin.GlobOptions{
			FileSystem: space, Permission: permission.AllowAll{}, Limits: builtin.SearchLimits{MaxOutputBytes: 1},
		})
		result, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":"**"}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "truncated") {
			t.Fatalf("output-limited result = %#v", result)
		}
	})
}

func TestGlobRejectsInvalidPatternAndNonDirectory(t *testing.T) {
	t.Parallel()

	space := populatedSearchSpace(t)
	glob := newGlob(t, builtin.GlobOptions{FileSystem: space, Permission: permission.AllowAll{}})
	_, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":"["}`))
	assertToolCode(t, err, "invalid_pattern")
	_, err = glob.Execute(context.Background(), json.RawMessage(`{"path":"src/main.go","pattern":"**"}`))
	assertToolCode(t, err, "not_directory")
}

func TestSearchDefaultsToDenyBeforeList(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{}
	glob := newGlob(t, builtin.GlobOptions{FileSystem: fileSystem})
	_, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":"**"}`))
	assertToolCode(t, err, "permission_denied")
	grep := newGrep(t, builtin.GrepOptions{FileSystem: fileSystem})
	_, err = grep.Execute(context.Background(), json.RawMessage(`{"pattern":"value"}`))
	assertToolCode(t, err, "permission_denied")
	if fileSystem.listCalls != 0 {
		t.Fatalf("denied searches listed the filesystem %d time(s)", fileSystem.listCalls)
	}
}

func TestSearchPropagatesPreCancellationBeforeList(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fileSystem := &fakeFileSystem{}
	glob := newGlob(t, builtin.GlobOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err := glob.Execute(ctx, json.RawMessage(`{"pattern":"**"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Glob Execute() error = %v", err)
	}
	grep := newGrep(t, builtin.GrepOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err = grep.Execute(ctx, json.RawMessage(`{"pattern":"value"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Grep Execute() error = %v", err)
	}
	if fileSystem.listCalls != 0 {
		t.Fatalf("canceled searches listed the filesystem %d time(s)", fileSystem.listCalls)
	}
}

func TestGrepFindsLinesAndReportsSkippedFiles(t *testing.T) {
	t.Parallel()

	space := populatedSearchSpace(t)
	grep := newGrep(t, builtin.GrepOptions{
		FileSystem: space, Permission: permission.AllowAll{}, Limits: builtin.SearchLimits{MaxFileBytes: 64},
	})
	result, err := grep.Execute(context.Background(), json.RawMessage(`{
		"path":"src","pattern":"TODO","include":"**/*.go"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 || result.Content[0].Text != "Found 2 matching line(s); skipped 2 unreadable, oversized, or non-UTF-8 file(s)." {
		t.Fatalf("result = %#v", result)
	}
	expected := `"src/main.go":2:TODO first` + "\n" + `"src/main.go":3:value // TODO second`
	if result.Content[1].Text != expected {
		t.Fatalf("matches = %q, want %q", result.Content[1].Text, expected)
	}
}

func TestGrepBoundsResultsAndValidatesPatterns(t *testing.T) {
	t.Parallel()

	space := populatedSearchSpace(t)
	grep := newGrep(t, builtin.GrepOptions{
		FileSystem: space, Permission: permission.AllowAll{}, Limits: builtin.SearchLimits{MaxResults: 1},
	})
	result, err := grep.Execute(context.Background(), json.RawMessage(`{"pattern":"TODO","include":"**/*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 || !strings.Contains(result.Content[0].Text, "truncated") || strings.Contains(result.Content[1].Text, "\n") {
		t.Fatalf("truncated result = %#v", result)
	}
	_, err = grep.Execute(context.Background(), json.RawMessage(`{"pattern":"["}`))
	assertToolCode(t, err, "invalid_pattern")
	_, err = grep.Execute(context.Background(), json.RawMessage(`{"pattern":"TODO","include":"["}`))
	assertToolCode(t, err, "invalid_pattern")
}

func TestSearchRejectsInvalidFileSystemEntriesAndRuntimeFailures(t *testing.T) {
	t.Parallel()

	malicious := &fakeFileSystem{listEntries: []safespace.Entry{{Name: "../outside", IsDir: false}}}
	glob := newGlob(t, builtin.GlobOptions{FileSystem: malicious, Permission: permission.AllowAll{}})
	_, err := glob.Execute(context.Background(), json.RawMessage(`{"pattern":"**"}`))
	assertRuntimeCode(t, err, "file_system_failed")

	failing := &fakeFileSystem{
		listEntries: []safespace.Entry{{Name: "file.txt", Path: "file.txt", Size: 4}},
		readErr:     errors.New("storage offline"),
	}
	grep := newGrep(t, builtin.GrepOptions{FileSystem: failing, Permission: permission.AllowAll{}})
	_, err = grep.Execute(context.Background(), json.RawMessage(`{"pattern":"value"}`))
	assertRuntimeCode(t, err, "file_system_failed")
}

func TestSearchConstructorsRejectInvalidDependenciesAndLimits(t *testing.T) {
	t.Parallel()

	if _, err := builtin.NewGlob(builtin.GlobOptions{}); err == nil {
		t.Fatal("NewGlob() accepted a nil filesystem")
	}
	if _, err := builtin.NewGrep(builtin.GrepOptions{}); err == nil {
		t.Fatal("NewGrep() accepted a nil filesystem")
	}
	fileSystem := &fakeFileSystem{}
	if _, err := builtin.NewGlob(builtin.GlobOptions{FileSystem: fileSystem, Limits: builtin.SearchLimits{MaxDepth: -1}}); err == nil {
		t.Fatal("NewGlob() accepted a negative limit")
	}
	if _, err := builtin.NewGrep(builtin.GrepOptions{FileSystem: fileSystem, Limits: builtin.SearchLimits{MaxOutputBytes: -1}}); err == nil {
		t.Fatal("NewGrep() accepted a negative limit")
	}
	if _, err := builtin.NewEdit(builtin.EditOptions{}); err == nil {
		t.Fatal("NewEdit() accepted a nil filesystem")
	}
	if _, err := builtin.NewEdit(builtin.EditOptions{FileSystem: fileSystem, MaxBytes: -1}); err == nil {
		t.Fatal("NewEdit() accepted a negative limit")
	}
}

func populatedSearchSpace(t *testing.T) *safespace.SafeSpace {
	t.Helper()
	space, err := safespace.OpenSafeSpace(t.TempDir(), safespace.Limits{
		MaxReadBytes: 1 << 20, MaxWriteBytes: 1 << 20, MaxListEntries: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = space.Close() })
	files := map[string][]byte{
		"root.txt":           []byte("TODO root\n"),
		"src/main.go":        []byte("package main\nTODO first\nvalue // TODO second\n"),
		"src/nested/test.go": []byte("package nested\n"),
		"src/readme.md":      []byte("TODO docs\n"),
		"src/binary.go":      {0xff, 0xfe},
		"src/large.go":       []byte(strings.Repeat("x", 100)),
	}
	for name, data := range files {
		if err := space.WriteFile(context.Background(), name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return space
}

func newGlob(t *testing.T, options builtin.GlobOptions) *builtin.GlobTool {
	t.Helper()
	value, err := builtin.NewGlob(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newGrep(t *testing.T, options builtin.GrepOptions) *builtin.GrepTool {
	t.Helper()
	value, err := builtin.NewGrep(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
