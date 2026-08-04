package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/mcp"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestClientDiscoversRegistersAndCallsRemoteTools(t *testing.T) {
	t.Parallel()

	session := &fakeSession{
		info: mcp.ServerInfo{Name: "test-server", Version: "1.0.0", ProtocolVersion: "2025-11-25"},
		pages: map[string]mcp.ToolPage{
			"":     {Tools: []model.ToolDefinition{definition("lookup")}, NextCursor: "next"},
			"next": {Tools: []model.ToolDefinition{definition("write")}},
		},
		callResult: mcp.CallResult{
			Content:           []message.Content{{Type: message.ContentText, Text: "remote result"}},
			StructuredContent: json.RawMessage(`{"count":1}`),
		},
	}
	client := newClient(t, mcp.ClientOptions{Connector: connector(session)})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	info, ok := client.ServerInfo()
	if !ok || info != session.info {
		t.Fatalf("ServerInfo() = %#v, %v", info, ok)
	}
	tools := client.Tools()
	if len(tools) != 2 || tools[0].Definition().Name != "lookup" || tools[1].Definition().Name != "write" {
		t.Fatalf("Tools() = %#v", tools)
	}
	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range tools {
		if err := kit.RegisterWith(toolkit.Registration{Tool: remote, Namespace: "remote", Groups: []string{"mcp"}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_1", Name: "remote.lookup", Arguments: json.RawMessage(`{"query":"value"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "remote result" || string(result.State) != `{"count":1}` {
		t.Fatalf("result = %#v", result)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.callName != "lookup" || string(session.callArguments) != `{"query":"value"}` {
		t.Fatalf("CallTool() = %q, %s", session.callName, session.callArguments)
	}
	if len(session.cursors) != 2 || session.cursors[0] != "" || session.cursors[1] != "next" {
		t.Fatalf("ListTools cursors = %#v", session.cursors)
	}
}

func TestClientReconnectSwapsSessionWithoutReplayingCalls(t *testing.T) {
	t.Parallel()

	first := standardSession("first")
	second := standardSession("second")
	var connectorCalls int
	client := newClient(t, mcp.ClientOptions{Connector: mcp.ConnectorFunc(func(context.Context) (mcp.Session, error) {
		connectorCalls++
		if connectorCalls == 1 {
			return first, nil
		}
		return second, nil
	})})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	registered := client.Tools()[0]
	if _, err := registered.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := client.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first.closeCalls != 1 || first.callCalls != 1 || second.callCalls != 0 {
		t.Fatalf("before replacement call: first=%#v second=%#v", first, second)
	}
	if _, err := registered.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if first.callCalls != 1 || second.callCalls != 1 {
		t.Fatalf("registered Tool did not use replacement: first calls %d, second calls %d", first.callCalls, second.callCalls)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if second.closeCalls != 1 {
		t.Fatalf("replacement Close calls = %d", second.closeCalls)
	}
}

func TestClientLifecycleStatesHealthAndIdempotentClose(t *testing.T) {
	t.Parallel()

	session := standardSession("server")
	client := newClient(t, mcp.ClientOptions{Connector: connector(session)})
	assertMCPCode(t, client.Health(context.Background()), mcp.ErrorNotConnected)
	assertMCPCode(t, client.Reconnect(context.Background()), mcp.ErrorNotConnected)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertMCPCode(t, client.Connect(context.Background()), mcp.ErrorInvalidConfig)
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.pingCalls != 1 {
		t.Fatalf("Ping calls = %d", session.pingCalls)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if session.closeCalls != 1 || len(client.Tools()) != 0 {
		t.Fatalf("closed state = closes %d, tools %d", session.closeCalls, len(client.Tools()))
	}
	assertMCPCode(t, client.Connect(context.Background()), mcp.ErrorClosed)
	assertMCPCode(t, client.Health(context.Background()), mcp.ErrorClosed)
	assertMCPCode(t, client.Reconnect(context.Background()), mcp.ErrorClosed)
}

func TestClientRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := mcp.NewClient(mcp.ClientOptions{}); err == nil {
		t.Fatal("NewClient() accepted a nil Connector")
	}
	validConnector := connector(standardSession("server"))
	for _, options := range []mcp.ClientOptions{
		{Connector: validConnector, Limits: mcp.Limits{MaxTools: -1}},
		{Connector: validConnector, Limits: mcp.Limits{MaxPages: -1}},
		{Connector: validConnector, Limits: mcp.Limits{MaxCatalogBytes: -1}},
		{Connector: validConnector, Limits: mcp.Limits{MaxArgumentBytes: -1}},
		{Connector: validConnector, Limits: mcp.Limits{MaxOutputBytes: -1}},
		{Connector: validConnector, Limits: mcp.Limits{MaxContentItems: -1}},
		{Connector: validConnector, Limits: mcp.Limits{ToolTimeout: -1}},
		{Connector: validConnector, ConcurrencyClass: "sometimes"},
	} {
		if _, err := mcp.NewClient(options); err == nil {
			t.Fatalf("NewClient(%#v) accepted invalid options", options)
		}
	}
	var nilConnector mcp.ConnectorFunc
	if _, err := nilConnector.Connect(context.Background()); err == nil {
		t.Fatal("ConnectorFunc(nil) returned no error")
	}
}

func TestClientRejectsInvalidDiscoveryResponsesAndLimits(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		session *fakeSession
		limits  mcp.Limits
		code    mcp.ErrorCode
	}{
		{name: "invalid info", session: &fakeSession{info: mcp.ServerInfo{}, pages: emptyPages()}, code: mcp.ErrorInvalidResponse},
		{name: "invalid definition", session: sessionWithPage(mcp.ToolPage{Tools: []model.ToolDefinition{{Name: "bad", InputSchema: json.RawMessage(`no`)}}}), code: mcp.ErrorInvalidResponse},
		{name: "unrepresentable name", session: sessionWithPage(mcp.ToolPage{Tools: []model.ToolDefinition{definition("-bad")}}), code: mcp.ErrorInvalidResponse},
		{name: "duplicate tool", session: sessionWithPages(map[string]mcp.ToolPage{"": {Tools: []model.ToolDefinition{definition("same")}, NextCursor: "n"}, "n": {Tools: []model.ToolDefinition{definition("same")}}}), code: mcp.ErrorInvalidResponse},
		{name: "cursor loop", session: sessionWithPages(map[string]mcp.ToolPage{"": {NextCursor: "n"}, "n": {NextCursor: "n"}}), code: mcp.ErrorInvalidResponse},
		{name: "tool limit", session: sessionWithPage(mcp.ToolPage{Tools: []model.ToolDefinition{definition("a"), definition("b")}}), limits: mcp.Limits{MaxTools: 1}, code: mcp.ErrorCatalogLimit},
		{name: "catalog limit", session: sessionWithPage(mcp.ToolPage{Tools: []model.ToolDefinition{definition("large")}}), limits: mcp.Limits{MaxCatalogBytes: 4}, code: mcp.ErrorCatalogLimit},
		{name: "page limit", session: sessionWithPages(map[string]mcp.ToolPage{"": {NextCursor: "n"}, "n": {}}), limits: mcp.Limits{MaxPages: 1}, code: mcp.ErrorCatalogLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, mcp.ClientOptions{Connector: connector(test.session), Limits: test.limits})
			assertMCPCode(t, client.Connect(context.Background()), test.code)
			if test.session.closeCalls != 1 {
				t.Fatalf("failed candidate Close calls = %d", test.session.closeCalls)
			}
		})
	}
}

func TestRemoteToolMapsBusinessFailuresTimeoutAndOutputLimits(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		result      mcp.CallResult
		call        func(context.Context) (mcp.CallResult, error)
		limits      mcp.Limits
		toolCode    string
		runtimeCode string
	}{
		{name: "remote error", result: mcp.CallResult{IsError: true, Content: []message.Content{{Type: message.ContentText, Text: "try another value"}}}, toolCode: "mcp_tool_error"},
		{name: "output limit", result: mcp.CallResult{Content: []message.Content{{Type: message.ContentText, Text: "12345"}}}, limits: mcp.Limits{MaxOutputBytes: 4}, toolCode: "mcp_output_limit"},
		{name: "invalid structured", result: mcp.CallResult{Content: []message.Content{{Type: message.ContentText, Text: "value"}}, StructuredContent: json.RawMessage(`bad`)}, runtimeCode: "invalid_mcp_response"},
		{name: "call failure", call: func(context.Context) (mcp.CallResult, error) { return mcp.CallResult{}, errors.New("transport failed") }, runtimeCode: "mcp_call_failed"},
		{name: "timeout", call: func(ctx context.Context) (mcp.CallResult, error) { <-ctx.Done(); return mcp.CallResult{}, ctx.Err() }, limits: mcp.Limits{ToolTimeout: 10 * time.Millisecond}, toolCode: "mcp_tool_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := standardSession("server")
			session.callResult = test.result
			if test.call != nil {
				session.callHook = func(ctx context.Context, _ string, _ json.RawMessage) (mcp.CallResult, error) { return test.call(ctx) }
			}
			client := newClient(t, mcp.ClientOptions{Connector: connector(session), Limits: test.limits})
			if err := client.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err := client.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
			if test.toolCode != "" {
				assertToolCode(t, err, test.toolCode)
			} else {
				assertRuntimeCode(t, err, test.runtimeCode)
			}
		})
	}
}

func TestRemoteToolSynthesizesContentAndPropagatesExternalCancellation(t *testing.T) {
	t.Parallel()

	session := standardSession("server")
	session.callResult = mcp.CallResult{StructuredContent: json.RawMessage(`{"value":true}`)}
	client := newClient(t, mcp.ClientOptions{Connector: connector(session)})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != `{"value":true}` {
		t.Fatalf("structured-only result = %#v", result)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Tools()[0].Execute(ctx, json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Execute() error = %v", err)
	}
}

func TestRemoteToolRejectsOversizedArgumentsBeforeSession(t *testing.T) {
	t.Parallel()

	session := standardSession("server")
	client := newClient(t, mcp.ClientOptions{
		Connector: connector(session), Limits: mcp.Limits{MaxArgumentBytes: 2},
	})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err := client.Tools()[0].Execute(context.Background(), json.RawMessage(`{"x":1}`))
	assertToolCode(t, err, "mcp_argument_limit")
	if session.callCalls != 0 {
		t.Fatal("oversized arguments reached Session")
	}
}

func TestClientContainsExtensionPanics(t *testing.T) {
	t.Parallel()

	client := newClient(t, mcp.ClientOptions{Connector: mcp.ConnectorFunc(func(context.Context) (mcp.Session, error) {
		panic("connect")
	})})
	assertMCPCode(t, client.Connect(context.Background()), mcp.ErrorConnectFailed)

	for _, test := range []struct {
		name         string
		set          func(*fakeSession)
		code         mcp.ErrorCode
		afterConnect bool
	}{
		{name: "server info", set: func(s *fakeSession) { s.infoPanic = true }, code: mcp.ErrorInvalidResponse},
		{name: "list", set: func(s *fakeSession) { s.listPanic = true }, code: mcp.ErrorDiscoveryFailed},
		{name: "ping", set: func(s *fakeSession) { s.pingPanic = true }, code: mcp.ErrorHealthFailed, afterConnect: true},
		{name: "call", set: func(s *fakeSession) { s.callPanic = true }, code: mcp.ErrorCallFailed, afterConnect: true},
		{name: "close", set: func(s *fakeSession) { s.closePanic = true }, code: mcp.ErrorCloseFailed, afterConnect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := standardSession("server")
			test.set(session)
			client := newClient(t, mcp.ClientOptions{Connector: connector(session)})
			if !test.afterConnect {
				assertMCPCode(t, client.Connect(context.Background()), test.code)
				return
			}
			if err := client.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch test.name {
			case "ping":
				assertMCPCode(t, client.Health(context.Background()), test.code)
			case "call":
				_, err := client.Tools()[0].Execute(context.Background(), json.RawMessage(`{}`))
				assertRuntimeCode(t, err, string(test.code))
			case "close":
				assertMCPCode(t, client.Close(), test.code)
			}
		})
	}
}

func definition(name string) model.ToolDefinition {
	return model.ToolDefinition{
		Name: name, Description: "test Tool", ConcurrencyClass: model.ToolConcurrencySafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"additionalProperties":false}`),
	}
}

func standardSession(name string) *fakeSession {
	return &fakeSession{
		info:       mcp.ServerInfo{Name: name, Version: "1.0.0", ProtocolVersion: "2025-11-25"},
		pages:      emptyPages(),
		callResult: mcp.CallResult{Content: []message.Content{{Type: message.ContentText, Text: name}}},
	}
}

func sessionWithPage(page mcp.ToolPage) *fakeSession {
	return sessionWithPages(map[string]mcp.ToolPage{"": page})
}

func sessionWithPages(pages map[string]mcp.ToolPage) *fakeSession {
	value := standardSession("server")
	value.pages = pages
	return value
}

func emptyPages() map[string]mcp.ToolPage {
	return map[string]mcp.ToolPage{"": {Tools: []model.ToolDefinition{definition("remote")}}}
}

func connector(session mcp.Session) mcp.Connector {
	return mcp.ConnectorFunc(func(context.Context) (mcp.Session, error) { return session, nil })
}

func newClient(t *testing.T, options mcp.ClientOptions) *mcp.Client {
	t.Helper()
	client, err := mcp.NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertMCPCode(t *testing.T, err error, code mcp.ErrorCode) {
	t.Helper()
	var value *mcp.Error
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want MCP code %q", err, code)
	}
}

func assertToolCode(t *testing.T, err error, code string) {
	t.Helper()
	var value *tool.Error
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want Tool code %q", err, code)
	}
}

func assertRuntimeCode(t *testing.T, err error, code string) {
	t.Helper()
	var value *tool.RuntimeError
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want Runtime code %q", err, code)
	}
}

type fakeSession struct {
	mu            sync.Mutex
	info          mcp.ServerInfo
	pages         map[string]mcp.ToolPage
	callResult    mcp.CallResult
	callErr       error
	callHook      func(context.Context, string, json.RawMessage) (mcp.CallResult, error)
	pingErr       error
	closeErr      error
	cursors       []string
	callName      string
	callArguments json.RawMessage
	callCalls     int
	pingCalls     int
	closeCalls    int
	infoPanic     bool
	listPanic     bool
	callPanic     bool
	pingPanic     bool
	closePanic    bool
}

func (f *fakeSession) ServerInfo() mcp.ServerInfo {
	if f.infoPanic {
		panic("server info")
	}
	return f.info
}

func (f *fakeSession) ListTools(ctx context.Context, cursor string) (mcp.ToolPage, error) {
	if f.listPanic {
		panic("list")
	}
	if err := ctx.Err(); err != nil {
		return mcp.ToolPage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, cursor)
	return f.pages[cursor].Clone(), nil
}

func (f *fakeSession) CallTool(ctx context.Context, name string, arguments json.RawMessage) (mcp.CallResult, error) {
	if f.callPanic {
		panic("call")
	}
	if err := ctx.Err(); err != nil {
		return mcp.CallResult{}, err
	}
	f.mu.Lock()
	f.callCalls++
	f.callName = name
	f.callArguments = append(json.RawMessage(nil), arguments...)
	hook := f.callHook
	result, err := f.callResult.Clone(), f.callErr
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, name, arguments)
	}
	return result, err
}

func (f *fakeSession) Ping(ctx context.Context) error {
	if f.pingPanic {
		panic("ping")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingCalls++
	return f.pingErr
}

func (f *fakeSession) Close() error {
	if f.closePanic {
		panic("close")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	return f.closeErr
}

var _ mcp.Session = (*fakeSession)(nil)
