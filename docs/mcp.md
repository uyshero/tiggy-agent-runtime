# MCP Client Semantics

## Scope and Construction

The `mcp` package imports remote MCP Tools into the native `tool.Tool` contract. It currently supports Tool discovery, Tool calls, Ping health checks, explicit reconnect, stdio, and Streamable HTTP. Prompts, resources as first-class APIs, sampling, roots, elicitation, and automatic multi-round-trip input are not enabled.

```go
connector, err := mcp.NewStdioConnector(mcp.StdioOptions{
    Implementation: mcp.Implementation{Name: "my-app", Version: "1.0.0"},
    Path:            "/absolute/path/to/mcp-server",
    Directory:       "/absolute/path/to/workspace",
    Environment:     []string{"API_TOKEN=" + token},
})
if err != nil {
    return err
}
client, err := mcp.NewClient(mcp.ClientOptions{Connector: connector})
if err != nil {
    return err
}
if err := client.Connect(ctx); err != nil {
    return err
}
defer client.Close()

for _, remote := range client.Tools() {
    if err := kit.RegisterWith(toolkit.Registration{
        Tool: remote, Namespace: "remote", Groups: []string{"mcp"},
    }); err != nil {
        return err
    }
}
```

The official MCP Go SDK is private implementation detail. Public contracts use Tiggy values and standard-library configuration only. See [ADR 0004](./adr/0004-official-mcp-go-sdk.md).

## Lifecycle

`NewClient` creates a disconnected Client. `Connect` negotiates one Session and completes bounded paginated Tool discovery before publishing any Tool. A failed connection or catalog validation closes the candidate and leaves the Client disconnected. `Health` sends Ping. `Close` waits for calls using the current Session, closes it, and permanently closes the Client; it is idempotent.

`Reconnect` connects and discovers a replacement before swapping Sessions. It does not retry or replay an in-flight or failed Tool Call. Existing Tool adapters call the replacement by their original remote name, but additions and removals appear only in a fresh `Tools` snapshot. Applications should rebuild Toolkit registration explicitly when a changed catalog matters.

There is no exactly-once guarantee. Cancellation or transport failure may occur after the server produced an external effect but before the result arrived. Automatic Tool-call replay is therefore forbidden.

## Limits

Zero-valued fields use these defaults:

```text
Tool catalog             256 Tools
discovery pages           32 pages
catalog schemas/text       2 MiB
Tool arguments           256 KiB
Tool result              128 KiB
result Content items       64
Tool-call timeout          60s
wire JSON/SSE event         4 MiB
```

Remote Tools are sequential by default. Applications may opt into `model.ToolConcurrencySafe` only when the server and underlying resources support overlapping calls. Toolkit independently compiles each imported Input Schema and applies Namespace and Group rules.

## Transports

Stdio requires an absolute executable and existing absolute working directory. It starts the process directly without a shell. A nil Environment means no environment variables; host secrets are not inherited. Arguments and Environment are copied. Applications may inject a Stderr writer for controlled diagnostics. Closing first closes protocol I/O, then interrupts and ultimately kills an unresponsive process within configured waits. This is process lifecycle management, not a sandbox: the server still has the host authority of the Runtime process.

Streamable HTTP requires HTTPS for non-loopback endpoints unless `AllowInsecureHTTP` is explicitly set. URL userinfo and fragments are rejected. Custom headers are copied and cannot override MCP protocol headers. Standalone SSE is disabled by default and must be enabled explicitly; SDK reconnection of an enabled SSE notification stream does not replay Tool calls. The caller owns a supplied `http.Client` and its Transport resources.

## Results and Failures

MCP text becomes text Content. Images become bounded image Data URLs. Resource Links and text Embedded Resources become text; embedded images become image Content. Audio and other binary resources return `unsupported_mcp_content` because the current Message protocol cannot represent them safely.

Structured content is retained as Tool Result state and becomes text when it is the only result. An empty successful result receives a non-empty completion summary. MCP `isError` results become model-actionable `mcp_tool_error`. Timeouts, argument/output limits, and unsupported content are also model-actionable and are never retried automatically.

Connection, discovery, Ping, protocol, invalid response, Session state, and extension panic failures use stable RuntimeError codes. Context cancellation propagates directly. A Session implementation that ignores Context still cannot be forcibly preempted by Core.

## External Compatibility

The default test suite is deterministic and does not require Node.js or network access. `make mcp-compat` is an explicit black-box stdio suite against independently implemented official TypeScript reference servers:

| Reference server | Pinned version | Contract exercised |
|---|---:|---|
| `@modelcontextprotocol/server-everything` | `2026.7.4` | initialize, Tool discovery, Ping, `echo`, text result, shutdown |
| `@modelcontextprotocol/server-filesystem` | `2026.7.10` | initialize, Tool discovery, Ping, confined `read_text_file`, structured result, shutdown |

The target requires a Unix-like host, Node.js, `npx`, and npm registry access when the pinned packages are not cached. It uses a temporary npm cache and passes only an explicit minimal environment to each server process. Missing `npx` is reported as a skipped compatibility test; it never changes the result of default `make verify`. A separate manual and weekly GitHub Actions workflow runs the network-dependent matrix with Node.js 22.

This matrix proves stdio interoperability for the listed versions and exercised contracts only. It does not certify every MCP capability or arbitrary third-party servers. Streamable HTTP remains covered by the official Go SDK compatibility test and local wire-limit tests; an independently implemented external HTTP server can be added when a stable non-interactive reference fixture is available.
