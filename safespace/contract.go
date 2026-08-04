// Package safespace defines bounded file execution environments for built-in Tools.
package safespace

import (
	"context"
	"fmt"
	"io/fs"
)

// Reader is the read-only FileSystem subset used by resource discovery.
type Reader interface {
	ReadFile(context.Context, string) ([]byte, error)
	List(context.Context, string) ([]Entry, error)
}

// FileSystem is the minimal file-operation contract consumed by tools. Paths
// use NormalizePath semantics. Implementations define their own containment,
// permission, and durability guarantees. Use SafeSpace when local access must
// remain under one root.
type FileSystem interface {
	Reader
	WriteFile(context.Context, string, []byte, fs.FileMode) error
}

type Entry struct {
	Name  string      `json:"name"`
	Path  string      `json:"path"`
	Mode  fs.FileMode `json:"mode"`
	Size  int64       `json:"size"`
	IsDir bool        `json:"is_dir"`
}

type ErrorCode string

const (
	ErrorInvalidPath  ErrorCode = "invalid_path"
	ErrorOutsideRoot  ErrorCode = "outside_root"
	ErrorNotRegular   ErrorCode = "not_regular_file"
	ErrorNotDirectory ErrorCode = "not_directory"
	ErrorReadLimit    ErrorCode = "read_limit"
	ErrorWriteLimit   ErrorCode = "write_limit"
	ErrorListLimit    ErrorCode = "list_limit"
	ErrorIO           ErrorCode = "io_failure"
)

type Error struct {
	Code      ErrorCode
	Operation string
	Path      string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return "safespace operation failed"
	}
	if e.Path == "" {
		return fmt.Sprintf("safespace %s failed (%s)", e.Operation, e.Code)
	}
	return fmt.Sprintf("safespace %s %q failed (%s)", e.Operation, e.Path, e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
