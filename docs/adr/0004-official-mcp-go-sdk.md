# ADR 0004: Official MCP Go SDK

- Status: Accepted
- Date: 2026-08-03

## Context

MCP requires protocol negotiation, JSON-RPC correlation, cancellation, pagination, stdio process lifecycle, Streamable HTTP, SSE reconnection, and evolving compatibility behavior. Reimplementing these protocol and transport layers would create avoidable correctness and security risk. Core must still retain Tiggy-owned lifecycle, limits, Tool semantics, and public API types.

## Decision

1. MCP transport compatibility uses `github.com/modelcontextprotocol/go-sdk` at `v1.7.0`.
2. SDK types remain private to `mcp`; exported Connector, Session, Tool, result, lifecycle, and configuration APIs use Tiggy or Go standard-library types.
3. `mcp.Client` owns explicit Connect, Ping-based Health, Reconnect, and idempotent Close. It never reconnects and replays a Tool Call automatically.
4. Tool discovery is paginated and bounded by page, Tool-count, and catalog-byte limits. Toolkit still compiles and validates every imported Input Schema.
5. stdio starts an explicitly configured absolute executable without a shell, uses an exact non-inherited environment, bounds each NDJSON message, and owns process termination.
6. Streamable HTTP requires HTTPS except for loopback or explicit opt-in, copies explicit headers, and bounds JSON responses or individual SSE events.
7. Client sampling, roots, elicitation, and automatic multi-round-trip behavior are disabled. This release adapts MCP Tools only.
8. MCP business errors remain model-actionable. Protocol, transport, invalid response, and SDK/extension panic failures terminate the Agent run.
9. Text, image, Resource Link, and representable Embedded Resource content are converted to Tiggy Content. Audio and non-image binary resources fail explicitly instead of being dropped.

## Dependency Assessment

- Implemented capability: MCP Tool discovery/calls over stdio and Streamable HTTP.
- Standard-library alternative: insufficient without implementing MCP negotiation, JSON-RPC, SSE, cancellation, and compatibility behavior locally.
- Version: `v1.7.0`, released 2026-07-27; requires Go 1.25.0, compatible with the Runtime baseline of Go 1.25.12.
- Ownership: official Model Context Protocol Go SDK under the `modelcontextprotocol` organization.
- License: the SDK is transitioning from MIT to Apache-2.0 and its repository LICENSE includes both grants plus CC-BY-4.0 for documentation. The Runtime is Apache-2.0 licensed; release review must continue to preserve the SDK's applicable notices and documentation attribution.
- Added transitive modules: `google/jsonschema-go`, `segmentio/encoding`, `segmentio/asm`, `yosida95/uritemplate`, `x/oauth2`, `x/sync`, `x/sys`, and `x/time`.
- CGO: none in the linked MCP dependency graph.
- Goroutines/resources: sessions own JSON-RPC read loops; Streamable HTTP may own an SSE reconnect loop; stdio owns a subprocess and wait goroutine. `mcp.Client.Close` is the required shutdown boundary.
- Public API exposure: none of the SDK's types.
- Security: `govulncheck v1.6.0` reported no vulnerabilities on 2026-08-03. The initial graph contained unreachable advisories in `x/text v0.14.0` and `x/sys v0.41.0`; the Runtime raised them to fixed `v0.39.0` and `v0.44.0`, respectively, rather than carrying an exception.
- Replacement path: replace private SDK Connector/Session conversion files while preserving `mcp.Connector`, `mcp.Session`, lifecycle tests, and external compatibility cases.

## Consequences

- Tiggy follows current MCP protocol behavior without owning a second implementation.
- The dependency graph and linked binary become materially larger, including OAuth packages not currently exposed by Tiggy.
- Wire-size limits are enforced outside the SDK before decoding for stdio, JSON HTTP responses, and SSE events.
- Same-SDK in-memory, stdio, and HTTP tests validate the adapter but do not constitute ecosystem compatibility. The opt-in external suite separately exercises pinned official TypeScript stdio servers.
- Tool-list change notifications do not mutate an existing Toolkit. Applications explicitly reconnect, fetch a new Tool snapshot, and rebuild registration when catalog changes are relevant.

## Rejected Alternatives

- Hand-written MCP/JSON-RPC transports: excessive compatibility, cancellation, and lifecycle risk.
- Exposing SDK ClientSession or Transport in Core APIs: leaks a third-party version contract into Tiggy's public surface.
- Automatic reconnect-and-retry around Tool calls: can duplicate external side effects after an ambiguous transport failure.
- Import every MCP capability now: expands authority and lifecycle scope beyond the Tool adapter before context and user-input policies are defined.
