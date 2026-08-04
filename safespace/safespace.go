package safespace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

var temporaryCounter atomic.Uint64

type Limits struct {
	MaxReadBytes   int64
	MaxWriteBytes  int64
	MaxListEntries int
}

func DefaultLimits() Limits {
	return Limits{MaxReadBytes: 4 << 20, MaxWriteBytes: 4 << 20, MaxListEntries: 10_000}
}

type SafeSpace struct {
	root      *os.Root
	rootPath  string
	limits    Limits
	closeOnce sync.Once
	closeErr  error
}

var _ FileSystem = (*SafeSpace)(nil)

func OpenSafeSpace(rootPath string, limits Limits) (*SafeSpace, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("safespace root is required")
	}
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if limits.MaxReadBytes <= 0 || limits.MaxWriteBytes <= 0 || limits.MaxListEntries <= 0 {
		return nil, fmt.Errorf("safespace limits must be positive")
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve safespace root: %w", err)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("open safespace root: %w", err)
	}
	return &SafeSpace{root: root, rootPath: absolute, limits: limits}, nil
}

func (w *SafeSpace) RootPath() string {
	if w == nil {
		return ""
	}
	return w.rootPath
}

func (w *SafeSpace) Close() error {
	if w == nil || w.root == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.closeErr = w.root.Close()
	})
	return w.closeErr
}

func (w *SafeSpace) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := NormalizePath(path)
	if err != nil {
		return nil, safeSpaceError("read", path, err)
	}
	file, err := w.root.Open(name)
	if err != nil {
		return nil, safeSpaceError("read", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, safeSpaceError("read", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, &Error{Code: ErrorNotRegular, Operation: "read", Path: path}
	}
	if info.Size() > w.limits.MaxReadBytes {
		return nil, &Error{Code: ErrorReadLimit, Operation: "read", Path: path}
	}
	data, err := io.ReadAll(io.LimitReader(file, w.limits.MaxReadBytes+1))
	if err != nil {
		return nil, safeSpaceError("read", path, err)
	}
	if int64(len(data)) > w.limits.MaxReadBytes {
		return nil, &Error{Code: ErrorReadLimit, Operation: "read", Path: path}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (w *SafeSpace) WriteFile(ctx context.Context, path string, data []byte, permission fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if int64(len(data)) > w.limits.MaxWriteBytes {
		return &Error{Code: ErrorWriteLimit, Operation: "write", Path: path}
	}
	name, err := NormalizePath(path)
	if err != nil || name == "." {
		if err == nil {
			err = errors.New("safespace root is not a file")
		}
		return safeSpaceError("write", path, err)
	}
	permission &= 0o777
	if permission == 0 {
		permission = 0o644
	}
	parent := filepath.Dir(name)
	if parent != "." {
		if err := w.root.MkdirAll(parent, 0o755); err != nil {
			return safeSpaceError("write", path, err)
		}
	}
	temporary := filepath.Join(parent, "."+filepath.Base(name)+".tiggy-"+randomSuffix())
	file, err := w.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
	if err != nil {
		return safeSpaceError("write", path, err)
	}
	committed := false
	defer func() {
		file.Close()
		if !committed {
			_ = w.root.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return safeSpaceError("write", path, err)
	}
	if err := file.Close(); err != nil {
		return safeSpaceError("write", path, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.root.Rename(temporary, name); err != nil {
		return safeSpaceError("write", path, err)
	}
	committed = true
	return nil
}

func (w *SafeSpace) List(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := NormalizePath(path)
	if err != nil {
		return nil, safeSpaceError("list", path, err)
	}
	directory, err := w.root.Open(name)
	if err != nil {
		return nil, safeSpaceError("list", path, err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return nil, safeSpaceError("list", path, err)
	}
	if !info.IsDir() {
		return nil, &Error{Code: ErrorNotDirectory, Operation: "list", Path: path}
	}
	entries, err := directory.ReadDir(w.limits.MaxListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, safeSpaceError("list", path, err)
	}
	if len(entries) > w.limits.MaxListEntries {
		return nil, &Error{Code: ErrorListLimit, Operation: "list", Path: path}
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := entry.Info()
		if err != nil {
			return nil, safeSpaceError("list", filepath.Join(path, entry.Name()), err)
		}
		entryPath := entry.Name()
		if name != "." {
			entryPath = filepath.Join(name, entry.Name())
		}
		result = append(result, Entry{
			Name: entry.Name(), Path: filepath.ToSlash(entryPath), Mode: info.Mode(), Size: info.Size(), IsDir: info.IsDir(),
		})
	}
	sort.Slice(result, func(first, second int) bool { return result[first].Name < result[second].Name })
	return result, nil
}

// NormalizePath converts a caller path to the relative, cleaned form used by
// FileSystem implementations. Empty paths address the root. Absolute paths
// and paths that escape through a parent are rejected.
func NormalizePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ".", nil
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", &Error{Code: ErrorInvalidPath, Cause: errors.New("null bytes are not allowed")}
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", &Error{Code: ErrorOutsideRoot, Cause: errors.New("absolute paths are not allowed")}
	}
	cleaned := filepath.Clean(path)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", &Error{Code: ErrorOutsideRoot, Cause: errors.New("parent traversal is not allowed")}
	}
	return filepath.ToSlash(cleaned), nil
}

func safeSpaceError(operation, path string, cause error) error {
	var existing *Error
	if errors.As(cause, &existing) {
		cloned := *existing
		cloned.Operation = operation
		cloned.Path = path
		return &cloned
	}
	code := ErrorIO
	if errors.Is(cause, os.ErrInvalid) {
		code = ErrorInvalidPath
	}
	return &Error{Code: code, Operation: operation, Path: path, Cause: cause}
}

func randomSuffix() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%x-%x", os.Getpid(), temporaryCounter.Add(1))
	}
	return hex.EncodeToString(value[:])
}
