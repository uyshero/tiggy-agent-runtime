package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	defaultMaxTools         = 256
	defaultMaxPages         = 32
	defaultMaxCatalogBytes  = 2 << 20
	defaultMaxArgumentBytes = 256 << 10
	defaultMaxOutputBytes   = 128 << 10
	defaultMaxContentItems  = 64
)

const defaultToolTimeout = 60 * time.Second

// Limits 限制远程发现与每次 Tool 调用；零值字段使用默认值。
type Limits struct {
	MaxTools         int
	MaxPages         int
	MaxCatalogBytes  int64
	MaxArgumentBytes int64
	MaxOutputBytes   int64
	MaxContentItems  int
	ToolTimeout      time.Duration
}

// DefaultLimits 返回偏保守的 MCP Client 边界。
func DefaultLimits() Limits {
	return Limits{
		MaxTools: defaultMaxTools, MaxPages: defaultMaxPages,
		MaxCatalogBytes: defaultMaxCatalogBytes, MaxOutputBytes: defaultMaxOutputBytes,
		MaxArgumentBytes: defaultMaxArgumentBytes,
		MaxContentItems:  defaultMaxContentItems, ToolTimeout: defaultToolTimeout,
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxTools == 0 {
		value.MaxTools = defaults.MaxTools
	}
	if value.MaxPages == 0 {
		value.MaxPages = defaults.MaxPages
	}
	if value.MaxCatalogBytes == 0 {
		value.MaxCatalogBytes = defaults.MaxCatalogBytes
	}
	if value.MaxOutputBytes == 0 {
		value.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if value.MaxArgumentBytes == 0 {
		value.MaxArgumentBytes = defaults.MaxArgumentBytes
	}
	if value.MaxContentItems == 0 {
		value.MaxContentItems = defaults.MaxContentItems
	}
	if value.ToolTimeout == 0 {
		value.ToolTimeout = defaults.ToolTimeout
	}
	if value.MaxTools < 0 || value.MaxPages < 0 || value.MaxCatalogBytes < 0 || value.MaxArgumentBytes < 0 || value.MaxOutputBytes < 0 || value.MaxContentItems < 0 || value.ToolTimeout < 0 {
		return Limits{}, fmt.Errorf("MCP limits must be positive")
	}
	return value, nil
}

// ClientOptions 配置生命周期，以及作为原生 Tool 暴露的定义。
// 默认并发类别为 sequential（保守）。
type ClientOptions struct {
	Connector        Connector
	Limits           Limits
	ConcurrencyClass model.ToolConcurrencyClass
}

type clientState uint8

const (
	stateDisconnected clientState = iota
	stateConnected
	stateClosed
)

// Client 持有当前 MCP Session 与不可变的已发现 Tool 快照。
// Connect / Reconnect / Close 串行；仅当 Definition 显式声明 safe 时，Tool 调用才可重叠。
type Client struct {
	lifecycle sync.Mutex
	mu        sync.RWMutex
	connector Connector
	limits    Limits
	class     model.ToolConcurrencyClass
	state     clientState
	session   Session
	info      ServerInfo
	tools     []tool.Tool
}

// NewClient 构造处于未连接状态的 MCP Client。
func NewClient(options ClientOptions) (*Client, error) {
	if options.Connector == nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("MCP connector is required")}
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	class := options.ConcurrencyClass
	if class == "" {
		class = model.ToolConcurrencySequential
	}
	if class != model.ToolConcurrencySequential && class != model.ToolConcurrencySafe {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("unsupported MCP Tool concurrency class %q", class)}
	}
	return &Client{connector: options.Connector, limits: limits, class: class}, nil
}

// Connect 建立并完整发现新 Session；发现失败会关闭候选 Session 并保持未连接。
func (c *Client) Connect(ctx context.Context) error {
	if ctx == nil {
		return &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("context is nil")}
	}
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	if state == stateClosed {
		return &Error{Code: ErrorClosed}
	}
	if state == stateConnected {
		return &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("MCP client is already connected")}
	}
	session, info, tools, err := c.connectAndDiscover(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.session, c.info, c.tools, c.state = session, info, tools, stateConnected
	c.mu.Unlock()
	return nil
}

// Reconnect creates and discovers a replacement before swapping sessions.
// Calls are never replayed automatically. Existing registered Tool adapters
// use the replacement session by remote name; callers must fetch Tools again
// to observe catalog additions or removals.
func (c *Client) Reconnect(ctx context.Context) error {
	if ctx == nil {
		return &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("context is nil")}
	}
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	if state == stateClosed {
		return &Error{Code: ErrorClosed}
	}
	if state != stateConnected {
		return &Error{Code: ErrorNotConnected}
	}
	replacement, info, tools, err := c.connectAndDiscover(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	previous := c.session
	c.session, c.info, c.tools = replacement, info, tools
	c.mu.Unlock()
	if err := closeSession(previous); err != nil {
		return &Error{Code: ErrorCloseFailed, Cause: err}
	}
	return nil
}

// Health sends an MCP ping over the current session.
func (c *Client) Health(ctx context.Context) error {
	if ctx == nil {
		return &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("context is nil")}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state == stateClosed {
		return &Error{Code: ErrorClosed}
	}
	if c.state != stateConnected || c.session == nil {
		return &Error{Code: ErrorNotConnected}
	}
	if err := pingSession(ctx, c.session); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return &Error{Code: ErrorHealthFailed, Cause: err}
	}
	return nil
}

// ServerInfo returns the current negotiated identity.
func (c *Client) ServerInfo() (ServerInfo, bool) {
	if c == nil {
		return ServerInfo{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info, c.state == stateConnected
}

// Tools returns a copy of the current immutable Tool adapter slice.
func (c *Client) Tools() []tool.Tool {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]tool.Tool(nil), c.tools...)
}

// Close permanently closes the Client. It waits for calls holding the current
// session lease, then closes the Session. Close is idempotent.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	c.mu.Lock()
	if c.state == stateClosed {
		c.mu.Unlock()
		return nil
	}
	session := c.session
	c.session, c.tools, c.info, c.state = nil, nil, ServerInfo{}, stateClosed
	c.mu.Unlock()
	if err := closeSession(session); err != nil {
		return &Error{Code: ErrorCloseFailed, Cause: err}
	}
	return nil
}

func (c *Client) connectAndDiscover(ctx context.Context) (Session, ServerInfo, []tool.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, ServerInfo{}, nil, err
	}
	session, err := connectSession(ctx, c.connector)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ServerInfo{}, nil, ctxErr
		}
		return nil, ServerInfo{}, nil, &Error{Code: ErrorConnectFailed, Cause: err}
	}
	if session == nil {
		return nil, ServerInfo{}, nil, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP connector returned a nil session")}
	}
	fail := func(code ErrorCode, cause error) (Session, ServerInfo, []tool.Tool, error) {
		return nil, ServerInfo{}, nil, &Error{Code: code, Cause: errors.Join(cause, closeSession(session))}
	}
	info, err := readServerInfo(session)
	if err != nil {
		return fail(ErrorInvalidResponse, err)
	}
	if err := info.Validate(); err != nil {
		return fail(ErrorInvalidResponse, err)
	}
	definitions, err := c.discover(ctx, session)
	if err != nil {
		var mcpError *Error
		if errors.As(err, &mcpError) && mcpError != nil {
			return fail(mcpError.Code, mcpError.Cause)
		}
		return fail(ErrorDiscoveryFailed, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(ErrorDiscoveryFailed, err)
	}
	tools := make([]tool.Tool, len(definitions))
	for index, definition := range definitions {
		tools[index] = &remoteTool{client: c, definition: definition.Clone(), remoteName: definition.Name}
	}
	return session, info, tools, nil
}

func (c *Client) discover(ctx context.Context, session Session) ([]model.ToolDefinition, error) {
	var definitions []model.ToolDefinition
	cursor := ""
	seenCursors := map[string]struct{}{"": {}}
	seenTools := make(map[string]struct{})
	var catalogBytes int64
	for pageIndex := 0; pageIndex < c.limits.MaxPages; pageIndex++ {
		page, err := listSessionTools(ctx, session, cursor)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			return nil, &Error{Code: ErrorDiscoveryFailed, Cause: err}
		}
		page = page.Clone()
		for _, definition := range page.Tools {
			definition.ConcurrencyClass = c.class
			if err := validateRemoteDefinition(definition); err != nil {
				return nil, &Error{Code: ErrorInvalidResponse, Cause: err}
			}
			if _, exists := seenTools[definition.Name]; exists {
				return nil, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("duplicate MCP tool %q", definition.Name)}
			}
			seenTools[definition.Name] = struct{}{}
			catalogBytes += int64(len(definition.Name) + len(definition.Description) + len(definition.InputSchema) + len(definition.OutputSchema))
			if len(definitions) >= c.limits.MaxTools || catalogBytes > c.limits.MaxCatalogBytes {
				return nil, &Error{Code: ErrorCatalogLimit, Cause: fmt.Errorf("MCP Tool catalog exceeded configured limits")}
			}
			definitions = append(definitions, definition.Clone())
		}
		if page.NextCursor == "" {
			return definitions, nil
		}
		if len(page.NextCursor) > 4096 {
			return nil, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP cursor exceeds 4096 bytes")}
		}
		if _, exists := seenCursors[page.NextCursor]; exists {
			return nil, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP pagination cursor repeated")}
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return nil, &Error{Code: ErrorCatalogLimit, Cause: fmt.Errorf("MCP Tool discovery exceeded page limit")}
}

func validateRemoteDefinition(definition model.ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if len(definition.Name) > 128 {
		return fmt.Errorf("MCP tool name exceeds 128 bytes")
	}
	for _, segment := range strings.Split(definition.Name, ".") {
		if segment == "" {
			return fmt.Errorf("MCP tool name %q contains an empty segment", definition.Name)
		}
		for index, char := range []byte(segment) {
			allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-'
			if !allowed || index == 0 && char == '-' {
				return fmt.Errorf("MCP tool name %q cannot be represented by Toolkit", definition.Name)
			}
		}
	}
	return nil
}

func (c *Client) call(ctx context.Context, name string, arguments json.RawMessage) (CallResult, error) {
	if int64(len(arguments)) > c.limits.MaxArgumentBytes {
		return CallResult{}, &Error{Code: ErrorArgumentLimit, Cause: fmt.Errorf("MCP Tool arguments exceeded configured limit")}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state == stateClosed {
		return CallResult{}, &Error{Code: ErrorClosed}
	}
	if c.state != stateConnected || c.session == nil {
		return CallResult{}, &Error{Code: ErrorNotConnected}
	}
	result, err := callSessionTool(ctx, c.session, name, append(json.RawMessage(nil), arguments...))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return CallResult{}, ctxErr
	}
	if err != nil {
		var mcpError *Error
		if errors.As(err, &mcpError) && mcpError != nil {
			return CallResult{}, mcpError
		}
		return CallResult{}, &Error{Code: ErrorCallFailed, Cause: err}
	}
	result = result.Clone()
	if len(result.Content) > c.limits.MaxContentItems {
		return CallResult{}, &Error{Code: ErrorOutputLimit, Cause: fmt.Errorf("MCP result has too many content items")}
	}
	if len(result.StructuredContent) > 0 && !json.Valid(result.StructuredContent) {
		return CallResult{}, &Error{Code: ErrorInvalidResponse, Cause: fmt.Errorf("MCP structured content is invalid JSON")}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return CallResult{}, &Error{Code: ErrorInvalidResponse, Cause: err}
	}
	if int64(len(encoded)) > c.limits.MaxOutputBytes {
		return CallResult{}, &Error{Code: ErrorOutputLimit, Cause: fmt.Errorf("MCP result exceeded configured output limit")}
	}
	return result, nil
}

func connectSession(ctx context.Context, connector Connector) (session Session, err error) {
	defer recoverMCPPanic("connector", &err)
	return connector.Connect(ctx)
}

func readServerInfo(session Session) (info ServerInfo, err error) {
	defer recoverMCPPanic("server info", &err)
	return session.ServerInfo(), nil
}

func listSessionTools(ctx context.Context, session Session, cursor string) (page ToolPage, err error) {
	defer recoverMCPPanic("tools/list", &err)
	return session.ListTools(ctx, cursor)
}

func callSessionTool(ctx context.Context, session Session, name string, arguments json.RawMessage) (result CallResult, err error) {
	defer recoverMCPPanic("tools/call", &err)
	return session.CallTool(ctx, name, arguments)
}

func pingSession(ctx context.Context, session Session) (err error) {
	defer recoverMCPPanic("ping", &err)
	return session.Ping(ctx)
}

func closeSession(session Session) (err error) {
	if session == nil {
		return nil
	}
	defer recoverMCPPanic("close", &err)
	return session.Close()
}

func recoverMCPPanic(operation string, err *error) {
	if recovered := recover(); recovered != nil {
		*err = fmt.Errorf("MCP %s panic: %v", operation, recovered)
	}
}
