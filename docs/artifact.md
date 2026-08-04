# Artifact Offload Semantics

## Scope

`artifact` keeps large successful Tool Results out of model context through caller-injected storage. It defines a portable payload, integrity-bound opaque reference, bounded default Offloader, and Agent lifecycle events. It does not provide a database or object-store implementation.

```go
offloader, err := artifact.NewBoundedOffloader(artifact.BoundedOffloaderOptions{
    Store: storeAdapter,
    MaxInlineBytes: 64 << 10,
    MaxArtifactBytes: 16 << 20,
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name: "assistant",
    Model: provider,
    ModelIdentity: identity,
    ArtifactOffloader: offloader,
})
```

Thresholds apply to the encoded envelope, not only visible text. Zero values select the defaults shown above; custom limits must satisfy `0 < MaxInlineBytes < MaxArtifactBytes`. Offload is a post-execution projection boundary, not a substitute for each Tool/MCP adapter bounding its own output allocation.

## Payload and Reference

`artifact.NewRequest` serializes the complete validated `message.ToolResult`, including Content, State, Call ID, and Tool name, as `tiggy.artifact.tool-result.v1`. `DecodeToolResult` strictly decodes the same envelope for authorized retrieval adapters.

Every Request includes the exact bytes, media type `application/vnd.tiggy.tool-result+json;version=1`, and a `sha256:<hex>` digest. The Offloader may keep it inline or return a Reference. Core accepts an offloaded Decision only when its media type, byte size, and digest match the Request.

An offloaded model-visible Result contains one `message.ContentArtifact`. Its `ObjectRefID` is an opaque identifier, not a URL. Signed URLs, query strings, fragments, credentials, and backend records are rejected from the protocol. State contains only offload metadata and the Reference; the original Result is recoverable from Store through its encoded payload.

## Store Contract

`artifact.Store.Put` receives an owned byte copy, Tool identity, media type, digest, and advisory idempotency key. It returns only an opaque Object ID. Runtime invokes Put once and never automatically retries or deletes:

- a timeout or cancellation may happen after storage accepted the object;
- retrying could duplicate an external effect;
- deleting after later Run failure could remove an object already exposed to another consumer.

The Store Adapter defines durability, scope/authorization, encryption, retention, retrieval, deduplication, and orphan cleanup. Its Object ID must remain resolvable for as long as retained Agent State can reference it. SafeSpace is not an implicit Artifact Store.

Core does not fetch Artifact content back into context. If a model must inspect the full value, the application must register an explicitly authorized retrieval Tool that resolves Object IDs through its own policy.

## Agent Behavior

Offload runs after a successful Tool execution and before Result projection. Model-actionable Tool errors remain inline. With no Offloader, existing inline behavior is unchanged.

```text
tool.result.offload.started
tool.result.offload.completed: inline | offloaded
tool.result.offload.failed: failed | canceled
```

Events carry Call/Tool identity, encoded byte size, media type, digest, Object ID, status, and fallback. They never carry result bytes. Inline and offloaded outcomes use `fallback=none`; all failures use `fail_closed`.

Store/Offloader errors and panics return `artifact/artifact_offload_failed`. Invalid Decisions return `artifact/invalid_artifact_offload`, size overflow returns `limit/artifact_payload_limit`, and cancellation returns `canceled/artifact_offload_canceled`. Agent appends a safe error Tool Result, closes the Tool Call, and terminates before another model call.

## Ownership and Non-goals

Requests, Put buffers, Decisions, projected Content, and decoded Results are copied across boundaries. `BoundedOffloader` is immutable and is concurrency-safe when its Store is. A custom Offloader used by a concurrently reused Agent must be concurrency-safe and honor Context.

Core does not choose a cloud SDK, bucket, database, tenant scope, encryption key, retention policy, signed-URL mechanism, retrieval API, or garbage collector. Those are Adapter or Enterprise responsibilities.
