package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOfficialSDKInMemoryCompatibility(t *testing.T) {
	t.Parallel()

	type input struct {
		Value string `json:"value"`
	}
	type output struct {
		Length int `json:"length"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "official-test", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo", Description: "Echo a value."}, func(_ context.Context, _ *mcpsdk.CallToolRequest, value input) (*mcpsdk.CallToolResult, output, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: value.Value}}}, output{Length: len(value.Value)}, nil
	})
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	connector, err := newSDKConnector(Implementation{Name: "tiggy-test", Version: "0.0.0"}, func() (mcpsdk.Transport, error) {
		return clientTransport, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{Connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if info, ok := client.ServerInfo(); !ok || info.Name != "official-test" || info.ProtocolVersion == "" {
		t.Fatalf("ServerInfo() = %#v, %v", info, ok)
	}
	tools := client.Tools()
	if len(tools) != 1 || tools[0].Definition().Name != "echo" {
		t.Fatalf("Tools() = %#v", tools)
	}
	result, err := tools[0].Execute(context.Background(), json.RawMessage(`{"value":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Fatalf("result Content = %#v", result.Content)
	}
	if string(result.State) != `{"length":5}` {
		t.Fatalf("result State = %s", result.State)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOfficialSDKStreamableHTTPCompatibility(t *testing.T) {
	t.Parallel()

	server := testSDKServer()
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	const token = "test-token"
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()
	connector, err := NewStreamableHTTPConnector(StreamableHTTPOptions{
		Implementation: Implementation{Name: "tiggy-http-test", Version: "0.0.0"},
		Endpoint:       httpServer.URL, Headers: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{Connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := client.Tools()[0].Execute(ctx, json.RawMessage(`{"value":"http"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "http" {
		t.Fatalf("HTTP result = %#v", result)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOfficialSDKStdioCompatibility(t *testing.T) {
	if _, err := os.Stat(".tiggy-mcp-stdio-helper"); err == nil {
		if os.Getenv("TIGGY_MCP_HOST_SECRET") != "" {
			os.Exit(3)
		}
		server := testSDKServer()
		if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, ".tiggy-mcp-stdio-helper"), []byte("helper"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIGGY_MCP_HOST_SECRET", "must-not-be-inherited")
	connector, err := NewStdioConnector(StdioOptions{
		Implementation: Implementation{Name: "tiggy-stdio-test", Version: "0.0.0"},
		Path:           executable, Args: []string{"-test.run=^TestOfficialSDKStdioCompatibility$"},
		Directory:         directory,
		TerminateDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{Connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := client.Tools()[0].Execute(ctx, json.RawMessage(`{"value":"stdio"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "stdio" {
		t.Fatalf("stdio result = %#v", result)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportConstructorsRejectInvalidConfiguration(t *testing.T) {
	t.Parallel()

	implementation := Implementation{Name: "test", Version: "1"}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	file := filepath.Join(directory, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, options := range []StdioOptions{
		{Implementation: Implementation{}, Path: executable, Directory: directory},
		{Implementation: implementation, Path: "relative", Directory: directory},
		{Implementation: implementation, Path: file, Directory: directory},
		{Implementation: implementation, Path: executable, Directory: "relative"},
		{Implementation: implementation, Path: executable, Directory: file},
		{Implementation: implementation, Path: executable, Directory: directory, Args: []string{"x\x00y"}},
		{Implementation: implementation, Path: executable, Directory: directory, Environment: []string{"INVALID"}},
		{Implementation: implementation, Path: executable, Directory: directory, Environment: []string{"A=1", "A=2"}},
		{Implementation: implementation, Path: executable, Directory: directory, MaxMessageBytes: -1},
		{Implementation: implementation, Path: executable, Directory: directory, TerminateDuration: -1},
	} {
		if _, err := NewStdioConnector(options); err == nil {
			t.Fatalf("NewStdioConnector(%#v) accepted invalid options", options)
		}
	}
	for _, options := range []StreamableHTTPOptions{
		{Implementation: Implementation{}, Endpoint: "https://example.com/mcp"},
		{Implementation: implementation, Endpoint: "ftp://example.com/mcp"},
		{Implementation: implementation, Endpoint: "http://example.com/mcp"},
		{Implementation: implementation, Endpoint: "https://user@example.com/mcp"},
		{Implementation: implementation, Endpoint: "https://example.com/mcp#fragment"},
		{Implementation: implementation, Endpoint: "https://example.com/mcp", Headers: http.Header{"Content-Type": []string{"text/plain"}}},
		{Implementation: implementation, Endpoint: "https://example.com/mcp", Headers: http.Header{"Bad Header": []string{"value"}}},
		{Implementation: implementation, Endpoint: "https://example.com/mcp", Headers: http.Header{"X-Test": []string{"bad\nvalue"}}},
		{Implementation: implementation, Endpoint: "https://example.com/mcp", MaxSSEReconnects: -2},
		{Implementation: implementation, Endpoint: "https://example.com/mcp", MaxMessageBytes: -1},
	} {
		if _, err := NewStreamableHTTPConnector(options); err == nil {
			t.Fatalf("NewStreamableHTTPConnector(%#v) accepted invalid options", options)
		}
	}
}

func TestBoundedLineReaderLimitsEachWireMessage(t *testing.T) {
	t.Parallel()

	valid := &boundedLineReadCloser{reader: io.NopCloser(strings.NewReader("1234\n5678\n")), maximum: 4}
	data, err := io.ReadAll(valid)
	if err != nil || string(data) != "1234\n5678\n" {
		t.Fatalf("valid read = %q, %v", data, err)
	}
	overflow := &boundedLineReadCloser{reader: io.NopCloser(strings.NewReader("12345\n")), maximum: 4}
	data, err = io.ReadAll(overflow)
	if err == nil || string(data) != "1234" {
		t.Fatalf("overflow read = %q, %v", data, err)
	}
}

func TestHTTPReadersBoundJSONAndIndividualSSEEvents(t *testing.T) {
	t.Parallel()

	total := &boundedTotalReadCloser{reader: io.NopCloser(strings.NewReader("12\n34")), maximum: 4}
	data, err := io.ReadAll(total)
	if err == nil || string(data) != "12\n3" {
		t.Fatalf("bounded JSON read = %q, %v", data, err)
	}
	events := &boundedEventReadCloser{reader: io.NopCloser(strings.NewReader("data:12\n\ndata:34\n\n")), maximum: 8}
	data, err = io.ReadAll(events)
	if err != nil || string(data) != "data:12\n\ndata:34\n\n" {
		t.Fatalf("bounded SSE read = %q, %v", data, err)
	}
	overflow := &boundedEventReadCloser{reader: io.NopCloser(strings.NewReader("data:123\n\n")), maximum: 8}
	data, err = io.ReadAll(overflow)
	if err == nil || string(data) != "data:123" {
		t.Fatalf("overflow SSE read = %q, %v", data, err)
	}
}

func TestSDKContentConversion(t *testing.T) {
	t.Parallel()

	image, include, err := convertSDKContent(&mcpsdk.ImageContent{MIMEType: "image/png", Data: []byte("png")})
	if err != nil || !include || image.Image == nil || image.Image.URL != "data:image/png;base64,cG5n" {
		t.Fatalf("image conversion = %#v, %v, %v", image, include, err)
	}
	resource, include, err := convertSDKContent(&mcpsdk.EmbeddedResource{Resource: &mcpsdk.ResourceContents{URI: "file:///note", Text: "hello"}})
	if err != nil || !include || !strings.Contains(resource.Text, "hello") {
		t.Fatalf("resource conversion = %#v, %v, %v", resource, include, err)
	}
	if _, _, err := convertSDKContent(&mcpsdk.AudioContent{MIMEType: "audio/wav", Data: []byte("wav")}); err == nil {
		t.Fatal("audio content was silently accepted")
	}
	if _, _, err := convertSDKContent(&mcpsdk.ImageContent{MIMEType: "text/plain", Data: []byte("bad")}); err == nil {
		t.Fatal("invalid image MIME type was accepted")
	}
}

func testSDKServer() *mcpsdk.Server {
	type input struct {
		Value string `json:"value"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "transport-test", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "echo"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, value input) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: value.Value}}}, nil, nil
	})
	return server
}
