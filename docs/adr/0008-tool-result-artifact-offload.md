# ADR 0008: Large Tool Result Artifact Offload

- Status: Accepted
- Date: 2026-08-03

## Context

Tool output can be valid yet too large to retain in every model request. Truncation destroys exact output, while placing storage concerns inside Tool implementations produces inconsistent protocols and hides context pressure. Core needs a portable boundary without selecting an Enterprise database, bucket, tenant model, credential mechanism, or durability guarantee.

Storage Put is an external side effect. Cancellation or transport failure may occur after the Store accepted bytes, so implicit retry or cleanup can duplicate or delete observable objects. References can also leak credentials when signed URLs are treated as IDs.

## Decision

1. Add a generic `message.ContentArtifact` carrying an opaque Object ID, name, media type, positive byte size, and SHA-256 digest.
2. Add `artifact.Store` and `artifact.Offloader` Ports. Core provides a standard-library-only `BoundedOffloader`, but no Store implementation.
3. Encode the complete validated Tool Result as `tiggy.artifact.tool-result.v1`, including Call ID, Tool name, Content, and State. Provide strict decoding for retrieval adapters.
4. Thresholds apply to encoded bytes. Defaults are 64 KiB inline and 16 MiB maximum Artifact size.
5. The Store receives owned bytes, digest, Tool identity, and an advisory idempotency key. It returns only an opaque Object ID.
6. Reject URLs, query strings, fragments, mismatched media/size/digest, and invalid custom Decisions.
7. Offload only successful Tool Results. Safe Tool errors remain directly model-actionable.
8. On success, replace inline Content and State with one digest-bound Artifact Reference before State/model projection.
9. Never retry Put, auto-delete, or auto-fetch. Failure and cancellation are fail-closed and terminate after appending an explicit error Tool Result.
10. Emit paired events with size, digest, Object ID, status, and fallback, but no result body.

## Consequences

- Large exact outputs no longer consume later context when an application configures a Store.
- Retained Agent State depends on the Store keeping references resolvable for the required lifetime.
- Store adapters own durability, authorization, encryption, retrieval, retention, deduplication, and orphan cleanup.
- Cancellation can leave an unreferenced stored object. The idempotency key enables Adapter policy but does not authorize Core retry.
- Provider adapters must support Artifact Content or reject it explicitly. Model retrieval requires a separately authorized Tool.
- The Message protocol gains an additive Content variant and the Event protocol gains three additive lifecycle types.

## Rejected Alternatives

- **Truncate large text:** loses exact data and may remove semantically important suffixes.
- **Store only from individual Tools:** duplicates policy and makes Native/MCP/Built-in behavior inconsistent.
- **Use SafeSpace as the default Store:** confuses root-confined file I/O with durability, authorization, and lifecycle guarantees.
- **Put signed URLs in Message:** leaks expiring credentials into State, events, and provider requests.
- **Automatically retry Put:** external storage effects are ambiguous after timeout or cancellation.
- **Automatically fetch on the next model call:** defeats context bounds and bypasses explicit retrieval authorization.
