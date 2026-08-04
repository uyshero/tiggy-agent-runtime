package safespace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/safespace"
)

func TestSafeSpaceReadWriteAndList(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	value, err := safespace.OpenSafeSpace(root, safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	if err := value.WriteFile(context.Background(), "notes/today.txt", []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := value.ReadFile(context.Background(), "notes/today.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile() = %q, %v", data, err)
	}
	entries, err := value.List(context.Background(), "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "notes/today.txt" || entries[0].IsDir {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestSafeSpaceRejectsPathEscapeAndExternalSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	value, err := safespace.OpenSafeSpace(root, safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	for _, path := range []string{"../outside.txt", outside, "escape"} {
		if _, err := value.ReadFile(context.Background(), path); err == nil {
			t.Fatalf("ReadFile(%q) escaped root", path)
		}
	}
}

func TestSafeSpaceEnforcesLimitsAndCancellation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := safespace.OpenSafeSpace(root, safespace.Limits{MaxReadBytes: 4, MaxWriteBytes: 4, MaxListEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	_, err = value.ReadFile(context.Background(), "large.txt")
	assertSafeSpaceCode(t, err, safespace.ErrorReadLimit)
	err = value.WriteFile(context.Background(), "write.txt", []byte("12345"), 0o600)
	assertSafeSpaceCode(t, err, safespace.ErrorWriteLimit)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := value.WriteFile(ctx, "canceled.txt", []byte("ok"), 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "canceled.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled write created a file: %v", err)
	}
}

func TestSafeSpaceRejectsNonRegularRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	value, err := safespace.OpenSafeSpace(root, safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	_, err = value.ReadFile(context.Background(), "directory")
	assertSafeSpaceCode(t, err, safespace.ErrorNotRegular)
}

func TestSafeSpaceRejectsListOfFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := safespace.OpenSafeSpace(root, safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	_, err = value.List(context.Background(), "file.txt")
	assertSafeSpaceCode(t, err, safespace.ErrorNotDirectory)
}

func TestSafeSpaceCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	value, err := safespace.OpenSafeSpace(t.TempDir(), safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizePath(t *testing.T) {
	t.Parallel()

	for input, expected := range map[string]string{
		"":               ".",
		"  notes/a.txt ": "notes/a.txt",
		"notes/../a.txt": "a.txt",
	} {
		actual, err := safespace.NormalizePath(input)
		if err != nil || actual != expected {
			t.Fatalf("NormalizePath(%q) = %q, %v; want %q", input, actual, err, expected)
		}
	}
	for _, input := range []string{"../outside", filepath.Join(string(filepath.Separator), "outside"), string([]byte{'a', 0, 'b'})} {
		if _, err := safespace.NormalizePath(input); err == nil {
			t.Fatalf("NormalizePath(%q) accepted an escaping path", input)
		}
	}
}

func assertSafeSpaceCode(t *testing.T, err error, code safespace.ErrorCode) {
	t.Helper()
	var safeSpaceError *safespace.Error
	if !errors.As(err, &safeSpaceError) || safeSpaceError.Code != code {
		t.Fatalf("error = %v, want safespace code %q", err, code)
	}
}
