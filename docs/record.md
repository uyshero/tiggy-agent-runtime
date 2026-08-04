# Run Recorder Semantics

## Purpose and Boundary

`record` is the Runtime's black-box journal. It converts the synchronous Version 1 Event stream into versioned, integrity-linked Entries that can be inspected and validated after a Run. It receives no Agent State, model request, Tool implementation, database identity, tenant record, or replay authority.

Recording is not execution replay. The package supports inspection and structural validation only. It never invokes a Model, Tool, Memory Port, or other external effect. Re-execution would require separate product policy for credentials, cost, non-determinism, permissions, and ambiguous prior side effects.

Applications create one concurrency-safe `Recorder` over a `Journal` and pass `Recorder.Sink` to `RunStream`/`Continue`, optionally composed with other observers. A direct Recorder is strict: validation, sanitization, or Journal failure follows Event Sink failure semantics. Wrapping it in `observe.BestEffort` explicitly chooses availability over complete recording and is inappropriate when recording is an audit requirement.

## Recording Protocol

Each `Entry` uses `tiggy.runtime.record.v1` and contains:

- Run ID and the owned Version 1 Event;
- whether the payload was redacted;
- the prior Entry digest for the same Run;
- a SHA-256 digest over all other Entry fields.

The first Entry must be an unparented `run.started` at Sequence 1. Sequence must remain contiguous, Event IDs must be unique, and every Parent ID must reference an earlier Event in the same Run. A terminal Event closes the Recording and must be last.

`Recording.Validate` accepts a structurally valid partial journal left by interruption. `ValidateComplete` additionally requires a terminal Event. Validation detects changed content, broken/reordered/missing links, duplicate IDs, invalid parents, and Events after termination.

The digest chain is an integrity checksum, not authentication or non-repudiation. Anyone able to rewrite the journal can recompute every digest. Enterprise audit storage that needs tamper evidence must add an application-owned signature/MAC, protected key management, immutable storage policy, and trusted timestamp.

## Privacy

`SafeSanitizer` is the default. It preserves reviewed lifecycle payloads but removes Model Delta text, reasoning, Tool-call fragments, and Provider Error bodies. It removes the entire payload of Tool Delta and unknown future Event types until their privacy contract is reviewed. Each changed Entry sets `payload_redacted=true`.

`FullSanitizer` must be selected explicitly and retains every payload byte. The caller then owns classification, user consent, encryption, access control, retention, deletion, and incident exposure. A custom Sanitizer may change only Payload; Recorder rejects changes to Event protocol, IDs, type, sequence, parent, Run ID, or timestamp. Sanitizer errors and panics are contained as strict Sink errors.

## Bounds and Concurrency

Recorder defaults to at most 1,024 concurrent active Runs, 100,000 Events per Run, and 1 MiB per sanitized Event. One Recorder may be shared by concurrent Agent Runs. It serializes validation, digest construction, and Journal Append calls so each Journal sees a deterministic accepted order. The Event stream remains synchronous; a blocked Journal blocks the Run callback and cannot be preempted by Core.

## Journals

`MemoryJournal` is bounded by total Entry count and encoded bytes, returns independent snapshots, never evicts, and loses all data at process exit. It is intended for tests and local diagnostics.

`JSONLJournal` writes one Entry per line and permits Entries from concurrent Runs to interleave. Digest chains remain per Run. The caller owns the Writer and all file permissions, buffering, flush/fsync, close, rotation, encryption, retention, and recovery. A Writer error can leave a partial final line; successful `Append` alone does not prove disk durability.

`ReadJSONL` bounds line size, total Entries, and Recording count, groups interleaved Entries by Run, and validates every chain. It returns incomplete Recordings by default for crash diagnostics; `RequireComplete` rejects them.

Journal Append is an external side effect. If a custom Journal returns an error after partially accepting an Entry, Core cannot determine whether the write committed and does not retry automatically.

## Non-goals

Core does not provide PostgreSQL schemas, object-store adapters, indexes, query APIs, tenant authorization, retention jobs, legal deletion, signatures, cross-process locks, durable queues, remote shipping, UI timelines, or model/Tool re-execution. Enterprise may implement `Journal` or ingest JSONL while keeping backend types and policy outside Core.
