// Package builtin 提供随 Runtime 附带的有界原生工具（读/写/编辑/搜索/Bash 等）。
package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/permission"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const maximumPathBytes = 4096 // 路径参数字节上限

// decodeArguments 解析工具入参 JSON：禁止未知字段，且只允许单个 JSON 值。
func decodeArguments(input json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return invalidArguments(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return invalidArguments(err)
	}
	return nil
}

// normalizedFilePath 规范化并拒绝指向 safespace 根目录的路径。
func normalizedFilePath(path string) (string, error) {
	if len(path) == 0 || len(path) > maximumPathBytes {
		return "", invalidArguments(fmt.Errorf("path must contain between 1 and %d bytes", maximumPathBytes))
	}
	normalized, err := safespace.NormalizePath(path)
	if err != nil || normalized == "." {
		if err == nil {
			err = fmt.Errorf("path identifies the safespace root")
		}
		return "", tool.NewError("invalid_path", "The file path is invalid or outside the allowed root.", false, err)
	}
	return normalized, nil
}

// authorize 执行权限检查；拒绝映射为模型可见错误，其它失败为 RuntimeError。
func authorize(ctx context.Context, policy permission.Policy, name string, action permission.Action, target string) error {
	err := permission.Check(ctx, policy, permission.Request{Tool: name, Action: action, Target: target})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var permissionError *permission.Error
	if !errors.As(err, &permissionError) || permissionError == nil {
		return &tool.RuntimeError{Code: "permission_check_failed", Cause: err}
	}
	if permissionError.Code == permission.ErrorDenied {
		return tool.NewError("permission_denied", "This file operation is not permitted.", false, err)
	}
	return &tool.RuntimeError{Code: string(permissionError.Code), Cause: err}
}

// fileSystemError 将 safespace / fs 错误映射为模型可见或 Runtime 错误。
func fileSystemError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, fs.ErrNotExist) {
		return tool.NewError("file_not_found", "The requested file does not exist.", false, err)
	}
	if errors.Is(err, fs.ErrPermission) {
		return tool.NewError("file_access_denied", "The file cannot be accessed with the current operating-system permissions.", false, err)
	}
	var safeSpaceError *safespace.Error
	if errors.As(err, &safeSpaceError) && safeSpaceError != nil {
		switch safeSpaceError.Code {
		case safespace.ErrorInvalidPath, safespace.ErrorOutsideRoot:
			return tool.NewError("invalid_path", "The file path is invalid or outside the allowed root.", false, err)
		case safespace.ErrorNotRegular:
			return tool.NewError("not_regular_file", "The requested path is not a regular file.", false, err)
		case safespace.ErrorNotDirectory:
			return tool.NewError("not_directory", "The requested search path is not a directory.", false, err)
		case safespace.ErrorReadLimit, safespace.ErrorWriteLimit:
			return tool.NewError("file_too_large", "The file exceeds the configured size limit.", false, err)
		}
	}
	return &tool.RuntimeError{Code: "file_system_failed", Cause: fmt.Errorf("%s file: %w", operation, err)}
}

func invalidArguments(cause error) *tool.Error {
	return tool.NewError("invalid_tool_arguments", "The tool arguments are invalid.", false, cause)
}

func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateExpectedDigest(value string) error {
	if value == "" {
		return nil
	}
	const prefix = "sha256:"
	if value != strings.TrimSpace(value) || !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return invalidArguments(fmt.Errorf("expected_digest must be a sha256 digest"))
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, prefix)); err != nil {
		return invalidArguments(fmt.Errorf("expected_digest must be a sha256 digest: %w", err))
	}
	return nil
}

func fileStateJSON(state any) json.RawMessage {
	data, err := json.Marshal(state)
	if err != nil {
		panic(fmt.Sprintf("builtin file state marshal: %v", err))
	}
	return data
}
