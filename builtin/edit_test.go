package builtin_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/builtin"
	"github.com/uyshero/tiggy-agent-runtime/permission"
)

func TestEditReplacesUniqueTextWithCanonicalPermissionTarget(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{readData: []byte("before old after")}
	var request permission.Request
	edit := newEdit(t, builtin.EditOptions{
		FileSystem: fileSystem,
		Permission: permission.PolicyFunc(func(_ context.Context, value permission.Request) (permission.Decision, error) {
			request = value
			return permission.DecisionAllow, nil
		}),
	})
	result, err := edit.Execute(context.Background(), json.RawMessage(`{
		"path":"notes/../file.txt","old_text":"old","new_text":"new"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Tool != builtin.EditToolName || request.Action != permission.ActionEdit || request.Target != "file.txt" {
		t.Fatalf("permission request = %#v", request)
	}
	if fileSystem.readPath != "file.txt" || fileSystem.writePath != "file.txt" || string(fileSystem.writeData) != "before new after" || fileSystem.writeMode != 0o644 {
		t.Fatalf("filesystem state = %#v", fileSystem)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `Replaced 1 occurrence(s) in "file.txt".` {
		t.Fatalf("result = %#v", result)
	}
}

func TestEditRequiresUniqueMatchUnlessReplaceAll(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{readData: []byte("old and old")}
	edit := newEdit(t, builtin.EditOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err := edit.Execute(context.Background(), json.RawMessage(`{
		"path":"file.txt","old_text":"old","new_text":"new"
	}`))
	assertToolCode(t, err, "ambiguous_match")
	if fileSystem.writeCalls != 0 {
		t.Fatal("ambiguous edit wrote the file")
	}
	result, err := edit.Execute(context.Background(), json.RawMessage(`{
		"path":"file.txt","old_text":"old","new_text":"new","replace_all":true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(fileSystem.writeData) != "new and new" || result.Content[0].Text != `Replaced 2 occurrence(s) in "file.txt".` {
		t.Fatalf("replace_all result = %#v, data = %q", result, fileSystem.writeData)
	}
}

func TestEditReportsNotFoundAndNoOpWithoutWriting(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{readData: []byte("current")}
	edit := newEdit(t, builtin.EditOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err := edit.Execute(context.Background(), json.RawMessage(`{
		"path":"file.txt","old_text":"missing","new_text":"new"
	}`))
	assertToolCode(t, err, "text_not_found")
	result, err := edit.Execute(context.Background(), json.RawMessage(`{
		"path":"file.txt","old_text":"current","new_text":"current"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if fileSystem.writeCalls != 0 || result.Content[0].Text != `No content change was needed in "file.txt".` {
		t.Fatalf("no-op edit result = %#v, writes = %d", result, fileSystem.writeCalls)
	}
}

func TestEditEnforcesTextAndResultLimits(t *testing.T) {
	t.Parallel()

	t.Run("input", func(t *testing.T) {
		fileSystem := &fakeFileSystem{}
		edit := newEdit(t, builtin.EditOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}, MaxBytes: 4})
		_, err := edit.Execute(context.Background(), json.RawMessage(`{
			"path":"file.txt","old_text":"12345","new_text":"x"
		}`))
		assertToolCode(t, err, "file_too_large")
		if fileSystem.readCalls != 0 {
			t.Fatal("oversized edit performed IO")
		}
	})

	t.Run("result", func(t *testing.T) {
		fileSystem := &fakeFileSystem{readData: []byte("aaaa")}
		edit := newEdit(t, builtin.EditOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}, MaxBytes: 5})
		_, err := edit.Execute(context.Background(), json.RawMessage(`{
			"path":"file.txt","old_text":"a","new_text":"bb","replace_all":true
		}`))
		assertToolCode(t, err, "file_too_large")
		if fileSystem.writeCalls != 0 {
			t.Fatal("oversized edit result was written")
		}
	})
}

func TestEditRejectsBinaryAndDefaultsToDeny(t *testing.T) {
	t.Parallel()

	fileSystem := &fakeFileSystem{readData: []byte{0xff, 0xfe}}
	edit := newEdit(t, builtin.EditOptions{FileSystem: fileSystem, Permission: permission.AllowAll{}})
	_, err := edit.Execute(context.Background(), json.RawMessage(`{
		"path":"file.txt","old_text":"x","new_text":"y"
	}`))
	assertToolCode(t, err, "unsupported_file_encoding")

	fileSystem = &fakeFileSystem{readData: []byte("x")}
	edit = newEdit(t, builtin.EditOptions{FileSystem: fileSystem})
	_, err = edit.Execute(context.Background(), json.RawMessage(`{
		"path":"file.txt","old_text":"x","new_text":"y"
	}`))
	assertToolCode(t, err, "permission_denied")
	if fileSystem.readCalls != 0 {
		t.Fatal("denied edit read the file")
	}
}

func newEdit(t *testing.T, options builtin.EditOptions) *builtin.EditTool {
	t.Helper()
	value, err := builtin.NewEdit(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
