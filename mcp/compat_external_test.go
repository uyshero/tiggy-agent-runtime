//go:build mcpcompat

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	everythingServerPackage = "@modelcontextprotocol/server-everything@2026.7.4"
	filesystemServerPackage = "@modelcontextprotocol/server-filesystem@2026.7.10"
)

func TestExternalMCPStdioCompatibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("external stdio compatibility currently requires a Unix npx executable")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx is required for external MCP compatibility tests")
	}
	npx, err = filepath.Abs(npx)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"npm_config_audit=false",
		"npm_config_cache=" + cache,
		"npm_config_fund=false",
		"npm_config_update_notifier=false",
	}

	t.Run("official TypeScript everything server", func(t *testing.T) {
		client := connectExternalServer(t, npx, environment, t.TempDir(), []string{
			"--yes", "--package=" + everythingServerPackage, "mcp-server-everything", "stdio",
		})
		assertServerName(t, client, "mcp-servers/everything")
		remote := findTool(t, client.Tools(), "echo")
		result, err := remote.Execute(testContext(t), json.RawMessage(`{"message":"tiggy compatibility"}`))
		if err != nil {
			t.Fatal(err)
		}
		assertSingleText(t, result, "Echo: tiggy compatibility")
		assertHealthyAndClose(t, client)
	})

	t.Run("official TypeScript filesystem server", func(t *testing.T) {
		root := t.TempDir()
		fixture := filepath.Join(root, "fixture.txt")
		const content = "external MCP filesystem compatibility\n"
		if err := os.WriteFile(fixture, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		client := connectExternalServer(t, npx, environment, root, []string{
			"--yes", "--package=" + filesystemServerPackage, "mcp-server-filesystem", root,
		})
		assertServerName(t, client, "secure-filesystem-server")
		remote := findTool(t, client.Tools(), "read_text_file")
		arguments, err := json.Marshal(map[string]string{"path": fixture})
		if err != nil {
			t.Fatal(err)
		}
		result, err := remote.Execute(testContext(t), arguments)
		if err != nil {
			t.Fatal(err)
		}
		assertSingleText(t, result, content)
		var state struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(result.State, &state); err != nil || state.Content != content {
			t.Fatalf("structured result = %s, %v", result.State, err)
		}
		assertHealthyAndClose(t, client)
	})
}

func connectExternalServer(t *testing.T, npx string, environment []string, directory string, args []string) *Client {
	t.Helper()
	connector, err := NewStdioConnector(StdioOptions{
		Implementation:    Implementation{Name: "tiggy-external-compat", Version: "0.0.0"},
		Path:              npx,
		Args:              args,
		Directory:         directory,
		Environment:       environment,
		TerminateDuration: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{Connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close external MCP client: %v", err)
		}
	})
	return client
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func assertServerName(t *testing.T, client *Client, expected string) {
	t.Helper()
	info, ok := client.ServerInfo()
	if !ok || info.Name != expected || strings.TrimSpace(info.ProtocolVersion) == "" {
		t.Fatalf("ServerInfo() = %#v, %v", info, ok)
	}
}

func findTool(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Definition().Name == name {
			return candidate
		}
	}
	t.Fatalf("external server did not advertise Tool %q", name)
	return nil
}

func assertSingleText(t *testing.T, result tool.Result, expected string) {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].Text != expected {
		t.Fatalf("Tool result Content = %#v", result.Content)
	}
}

func assertHealthyAndClose(t *testing.T, client *Client) {
	t.Helper()
	if err := client.Health(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}
