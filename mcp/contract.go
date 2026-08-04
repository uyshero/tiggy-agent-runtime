// Package mcp adapts Model Context Protocol servers into native Tools.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

// Connector creates a fresh logical MCP session on each call. Implementations
// must not reuse a one-shot transport across calls.
type Connector interface {
	Connect(context.Context) (Session, error)
}

// ConnectorFunc adapts a function to Connector.
type ConnectorFunc func(context.Context) (Session, error)

func (f ConnectorFunc) Connect(ctx context.Context) (Session, error) {
	if f == nil {
		return nil, fmt.Errorf("MCP connector function is nil")
	}
	return f(ctx)
}

// Session is one initialized MCP connection. Implementations shared by calls
// must be concurrency-safe and honor Context cancellation. Close must be
// idempotent and unblock pending operations.
type Session interface {
	ServerInfo() ServerInfo
	ListTools(context.Context, string) (ToolPage, error)
	CallTool(context.Context, string, json.RawMessage) (CallResult, error)
	Ping(context.Context) error
	Close() error
}

// ServerInfo is the negotiated identity of one MCP session.
type ServerInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
}

func (i ServerInfo) Validate() error {
	if strings.TrimSpace(i.Name) == "" || strings.TrimSpace(i.Name) != i.Name {
		return fmt.Errorf("MCP server name is required without surrounding whitespace")
	}
	if strings.TrimSpace(i.Version) == "" || strings.TrimSpace(i.ProtocolVersion) == "" {
		return fmt.Errorf("MCP server and protocol versions are required")
	}
	if len(i.Name) > 256 || len(i.Version) > 128 || len(i.ProtocolVersion) > 128 {
		return fmt.Errorf("MCP server identity exceeds configured bounds")
	}
	return nil
}

// ToolPage is one page from tools/list. Definitions and schema buffers are
// owned by the caller.
type ToolPage struct {
	Tools      []model.ToolDefinition
	NextCursor string
}

// Clone returns a deep copy safe to retain after the Session call returns.
func (p ToolPage) Clone() ToolPage {
	cloned := ToolPage{NextCursor: p.NextCursor, Tools: make([]model.ToolDefinition, len(p.Tools))}
	for index, definition := range p.Tools {
		cloned.Tools[index] = definition.Clone()
	}
	return cloned
}

// CallResult is the portable subset of an MCP tools/call result. Structured
// content remains JSON and is also carried as Tool Result state.
type CallResult struct {
	Content           []message.Content
	StructuredContent json.RawMessage
	IsError           bool
}

// Clone returns an owned copy safe across asynchronous boundaries.
func (r CallResult) Clone() CallResult {
	r.Content = message.CloneContent(r.Content)
	r.StructuredContent = append(json.RawMessage(nil), r.StructuredContent...)
	return r
}

// ErrorCode classifies MCP lifecycle, protocol, and resource failures.
type ErrorCode string

const (
	ErrorInvalidConfig      ErrorCode = "invalid_mcp_config"
	ErrorNotConnected       ErrorCode = "mcp_not_connected"
	ErrorClosed             ErrorCode = "mcp_client_closed"
	ErrorConnectFailed      ErrorCode = "mcp_connect_failed"
	ErrorDiscoveryFailed    ErrorCode = "mcp_discovery_failed"
	ErrorHealthFailed       ErrorCode = "mcp_health_failed"
	ErrorCallFailed         ErrorCode = "mcp_call_failed"
	ErrorCloseFailed        ErrorCode = "mcp_close_failed"
	ErrorInvalidResponse    ErrorCode = "invalid_mcp_response"
	ErrorCatalogLimit       ErrorCode = "mcp_catalog_limit"
	ErrorArgumentLimit      ErrorCode = "mcp_argument_limit"
	ErrorOutputLimit        ErrorCode = "mcp_output_limit"
	ErrorUnsupportedContent ErrorCode = "unsupported_mcp_content"
)

// Error preserves a diagnostic cause without placing it in the stable message.
type Error struct {
	Code  ErrorCode
	Cause error
}

func (e *Error) Error() string {
	if e == nil {
		return "MCP operation failed"
	}
	return fmt.Sprintf("MCP operation failed (%s)", e.Code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
