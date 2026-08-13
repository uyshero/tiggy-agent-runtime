# Orchestrator

The `orchestrator` package provides a bounded, in-process execution layer above
`agent.Agent`.

It reads a validated `Brief`, executes `TaskSpec` values in slice order, passes
only explicitly referenced output artifacts to later tasks, validates each
output, runs an optional `Verifier`, and returns a complete `Report`. A task
failure or verification failure stops later execution and marks those tasks as
`skipped`.

```go
brief := orchestrator.Brief{
    ID: "release-1",
    Goal: "prepare a release note",
    Tasks: []orchestrator.TaskSpec{
        {
            ID: "collect",
            Objective: "collect the changes",
            Subagent: "writer",
            Output: orchestrator.ArtifactSpec{
                Name: "changes.json", MediaType: "application/json",
            },
            Verification: &orchestrator.VerificationSpec{
                Verifier: orchestrator.VerifierJSONSchema,
                Config: json.RawMessage(`{"type":"object"}`),
            },
        },
        {
            ID: "review",
            Objective: "review the collected changes",
            Subagent: "reviewer",
            InputArtifacts: []orchestrator.ArtifactInput{{TaskID: "collect"}},
            Output: orchestrator.ArtifactSpec{
                Name: "review.json", MediaType: "application/json",
            },
        },
    },
}

report, err := (orchestrator.Orchestrator{
    Subagents: orchestrator.Subagents{
        "writer":  writerSubagent,
        "reviewer": reviewerSubagent,
    },
    Verifiers: orchestrator.BuiltinVerifiers(),
    Verifier: releaseVerifier,
}).Run(ctx, brief)
```

Each task may select a named verifier. Registered task verifiers take
precedence over the compatibility-wide `Verifier`. Built-ins include an
offline Draft 2020-12 JSON Schema verifier and a deterministic text verifier
for prefix, suffix, required substrings, and byte bounds. An unknown verifier
fails before the Subagent is created.

For task-level isolation, register a `SubagentFactory` instead of a shared
Subagent. The factory is called once per task with the Execution ID and an
owned `TaskSpec`. It can create a new Agent with task-specific Memory Scope,
Toolkit groups, SafeSpace root, limits, and credentials. The same Brief may be
run repeatedly; each report and event stream has a separate Execution ID.

`ReadBrief` strictly decodes one JSON value, rejects unknown fields, duplicate
object keys at any nesting level, and trailing data, then validates task order
before execution. Verifier configs use the same duplicate-key rule even when a
Brief is constructed directly as Go values. `RunWithSink` emits a separate
versioned Orchestrator event stream. Event protocol v2 assigns one
`InvocationID` to each `Run` or `Resume`; sequence numbers start at 1 and are
monotonic within that invocation. The stable ordering identity is therefore
`(ExecutionID, InvocationID, Sequence)`. The Sink is synchronous and
fail-closed; its events contain artifact digests, not artifact bodies. Event
protocol v1 remains accepted by `Event.Validate` for stored v0.2.2 events.

Failures return `*RunError` with stable `ErrorClass` and code values. Report and
Event JSON contain only the safe `ErrorPayload`; the original cause remains on
the Go error chain and is not serialized. Completed, failed, and canceled runs
emit exactly one Orchestrator terminal event while the Sink remains available.
If the Sink itself fails, execution stops immediately because terminal delivery
can no longer be guaranteed.

`Artifact` owns its byte buffers and carries a SHA-256 digest. `NewArtifact`
should be used by Subagents so the digest is computed at the boundary. The
Orchestrator clones artifacts before passing them to another task and before
calling a verifier.

Configure the Orchestrator `Store` to persist each verified output before the
next task starts. In this mode, `Report.ArtifactReferences` and each completed
`TaskReport.ArtifactReference` contain opaque IDs, media type, exact size, and
SHA-256 digest; artifact bodies are omitted from the Report. A downstream task
loads its declared inputs through `Store.Get`, then the Orchestrator checks the
reference, digest, size, media type, metadata, and original output contract
again before handing owned bytes to the Subagent. Without a Store, the existing
in-process mode keeps owned values in `Report.Artifacts`.

`ArtifactLimits` defaults to 16 MiB per artifact and 64 MiB across one
Execution. `InMemoryStore` is a bounded, concurrent, process-local reference
implementation with idempotent Put behavior and no eviction. Store calls are
never retried implicitly: a failed or canceled Put may have completed its
external effect, so a durable adapter must enforce the supplied idempotency key.
Authorization, tenant isolation, encryption, retention, and garbage collection
remain Store adapter responsibilities.

## Checkpoints and recovery

Configure both `Checkpoints` and `Store` to make verified task progress
recoverable. Checkpoints never contain artifact bodies. `ExecutionSnapshot`
records the protocol and Runtime versions, Execution and Brief identities, a
SHA-256 digest of the validated Brief, the next task index, ordered completed
task references, the cumulative artifact byte count, and a monotonically
increasing version. Version 1 is the initial empty snapshot; each completed
task advances the version by one.

The commit order for each task is fixed:

1. Verify the Subagent output.
2. Persist the Artifact with `Store.Put`.
3. Commit the next snapshot with `CheckpointStore.CompareAndSwap`.
4. Emit `task.completed`.
5. Start the next task.

The initial empty checkpoint is committed before any lifecycle event or task
execution. A failed compare-and-swap therefore never reports the task as
completed. The Artifact Put may already have succeeded, so recovery retries use
the same per-Execution, per-task idempotency key and Store adapters must honor
it.

Call `Resume` with the original Execution ID and the same Brief. Recovery
validates the checkpoint protocol, Runtime version, Brief digest, progress,
budgets, and every referenced Artifact through `Store.Get`. It reconstructs
completed task reports from references and begins at `NextTask`; a completed
snapshot returns without resolving any Subagent. Resume events keep the same
Execution ID, receive a new Invocation ID, and begin that invocation's sequence
at 1. The returned Report carries both identities.

`InMemoryCheckpointStore` is a bounded, concurrent, process-local reference
implementation. Compare-and-swap prevents two workers from committing the same
checkpoint version, but it does not stop both workers from entering a Subagent
before one commit wins. Exclusive worker leases, durable scheduling, retry
policy, and abandoned Artifact cleanup remain application or Enterprise
responsibilities.

The managed Worker integration, lease/fencing requirements, and repository
dependency direction are specified separately in
[Orchestrator Enterprise integration](./orchestrator-enterprise.md).

`AgentSubagent` adapts an existing `agent.Agent`: it creates a fresh Agent Run
for the task and uses final Assistant text as the output artifact body. Its
default `TextInputProjector` passes bounded UTF-8 text and JSON artifact content
to the model as explicitly untrusted data. Unsupported binary media, invalid
JSON, digest mismatch, and projection overflow fail closed. Large or binary
artifacts require an application-provided projector or an authorized retrieval
Tool.
This is logical context isolation only. Process, filesystem, credential,
network, durable queue, worker lease, and container isolation remain application
or Enterprise responsibilities.
