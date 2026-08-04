// Package safespace 为内置工具定义有界文件执行环境。
package safespace

import (
	"context"
	"fmt"
	"io/fs"
)

// Reader 是资源发现使用的只读 FileSystem 子集。
type Reader interface {
	ReadFile(context.Context, string) ([]byte, error)
	List(context.Context, string) ([]Entry, error)
}

// FileSystem 是工具消费的最小文件操作契约。
// 路径遵循 NormalizePath 语义；实现自行保证隔离、权限与持久性。
// 需要本地访问始终落在单一根目录下时，使用 SafeSpace。
type FileSystem interface {
	Reader
	WriteFile(context.Context, string, []byte, fs.FileMode) error
}

// Entry 描述一次 List 返回的目录项。
type Entry struct {
	Name  string      `json:"name"`
	Path  string      `json:"path"`
	Mode  fs.FileMode `json:"mode"`
	Size  int64       `json:"size"`
	IsDir bool        `json:"is_dir"`
}

// ErrorCode 对 safespace 失败做稳定分类。
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

// Error 保留操作、路径与底层 Cause。
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
