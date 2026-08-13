# Model Contract Notes

`model.Model` is the only required provider execution contract. Optional token
counting uses the separate `model.TokenCounter` interface. Provider credentials,
client lifecycle, model catalogs, and deployment selection remain adapter or
application responsibilities.

## Tool Output Schema

`model.ToolDefinition.OutputSchema` may be supplied by MCP or another provider
catalog. Runtime preserves it when cloning definitions, computing Toolkit
definition digests, recording content-free definition byte counts, and passing
definitions through compatible adapters.

Runtime does not validate `tool.Result` against `OutputSchema`. A Tool Result is
a sequence of typed Message Content blocks plus optional opaque JSON State; it
is not one JSON instance with an unambiguous Schema target. Applications that
own a structured Tool contract may validate the structured value inside the
Tool or its adapter before constructing a Result.

## Capabilities

The legacy `model.Capabilities` value is descriptive only and is deprecated.
Agent does not use it to choose a model, expose Tools, change request shape,
select a fallback, or bypass validation. Until a versioned capability discovery
and selection protocol is accepted, adapters should keep provider catalog
metadata private and applications should make explicit configuration choices.

This avoids turning provider-specific flags into implicit Runtime policy and
keeps Enterprise tenant, entitlement, routing, and deployment records outside
Core.
