# Toolkit Semantics

## Registration and Snapshots

Toolkit is a concurrency-safe registry. Registration reads the Tool Definition once, compiles its Input Schema, and stores an owned Definition copy. Each Agent run receives an immutable View; later registrations affect later runs only.

```go
kit.RegisterWith(toolkit.Registration{
    Tool: nativeTool,
    Namespace: "crm",
    Groups: []string{"read", "customer"},
})
```

If the Tool's original name is `lookup`, its model-visible name is `crm.lookup`. Names and namespaces are bounded ASCII identifiers containing letters, digits, `_`, `-`, and `.` with no empty dot-separated segments.

Groups are capability projections and do not enter the Tool Call protocol. `SnapshotGroups` returns the union of requested groups and rejects unknown groups. `agent.Config.ToolGroups` limits one Agent to the selected tools.

MCP clients return ordinary `tool.Tool` adapters from `mcp.Client.Tools`. Register them with an explicit Namespace to prevent collisions between servers. Toolkit owns only its immutable registration snapshot; the application owns the MCP Client lifecycle and must rebuild registration after a catalog-changing reconnect.

## JSON Schema

- Input Schemas compile at registration using Draft 2020-12.
- Format assertions are enabled and content assertions are disabled.
- `$defs` and local fragments in the same Schema are supported.
- File and remote `$ref` values are rejected; compilation performs no external resource access.
- Schema-invalid arguments return model-actionable `invalid_tool_arguments` errors.
- Schema compiler and validator panics are contained rather than crashing the host process.
- Output Schema enforcement is deferred until the structured Tool output contract is defined.

See [ADR 0002](./adr/0002-json-schema-validation.md) for the dependency decision.

## Concurrency and Batches

Tool Definition `ConcurrencyClass` supports:

- empty or `sequential`: the conservative default and an exclusive batch barrier;
- `safe`: the Tool implementation and external resource permit overlapping calls.

`View.CallBatch` executes consecutive `safe` calls through bounded workers. A sequential Tool waits for the previous concurrent phase and prevents the next phase from starting early. Concurrency defaults to 4 with a hard maximum of 64. Results always preserve input Call order.

The batch copies calls and argument buffers before starting workers and waits for every worker before returning. Cancellation propagates through Context. The runtime cannot forcibly terminate a Tool that ignores Context.

The Agent loop still executes Tool Calls sequentially. Agent-level concurrency requires an explicit partial-failure and side-effect policy; adding the Toolkit batch API does not silently change existing Run semantics.

## Errors and Ownership

- Schema and model-correctable business failures return `tool.Error`.
- Ordinary errors, invalid Tool Results, Tool panics, and schema runtime failures return `tool.RuntimeError`.
- A Tool-supplied `tool.RuntimeError` is preserved instead of being collapsed into a generic execution failure.
- Toolkit copies argument and result buffers but neither copies nor closes caller-injected Tool instances.
- A Tool marked `safe` must support concurrent use across Agent runs and batch workers.
