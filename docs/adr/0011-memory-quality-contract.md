# ADR 0011: Memory Scope, Expiry, Provenance, and ID Conflict

- Status: Accepted
- Date: 2026-08-03

## Context

Recall is model-visible evidence, so a stale or incorrectly scoped Item can change Agent behavior even when the runtime itself remains valid. Source labels alone do not let diagnostics link an admitted Item back to the Run and Messages that produced it. The initial InMemory Adapter also rejected every duplicate ID, which is safe but too rigid for deterministic local simulations of deduplication or correction.

Core needs structural quality rules without claiming semantic truth, choosing tenant types, or building a database lifecycle API.

## Decision

1. Keep `Scope{Namespace, Key}` application-defined and require exact equality at Recall, projection, and Observe Admission boundaries.
2. Extend Provenance with optional Run ID and up to 64 unique Message IDs. Message IDs require a Run ID.
3. For Observe Admission, require Provenance Run ID to equal the observed Run and require at least one source Message ID from that completed turn.
4. Add optional `Item.ExpiresAt`. It must be a non-zero UTC timestamp and expires inclusively at `now >= ExpiresAt`.
5. Agent filters expired Items returned by every Memory Port before model projection and emits only an expired count. InMemory also filters on Recall and prunes expired Items before commit.
6. Add an injectable InMemory clock for deterministic expiry tests. Clock panic retains fatal extension classification.
7. Add three exact-ID conflict policies within one Scope: reject the batch by default, keep the existing Item, or atomically replace it and append the new Item as newest.
8. Keep batch-local duplicate IDs invalid under all policies. Never merge text, infer equivalence, or resolve contradictions semantically.
9. Deep-copy provenance Message IDs and expiry timestamps across all public boundaries.
10. Do not expose Scope key, expiry timestamp, or Run/Message provenance IDs in Memory events or model projection.

## Consequences

- Expired evidence cannot reach a model even when a custom Adapter forgets to filter it.
- Admitted local Items have a verifiable structural link to their observed source messages.
- Exact-ID replacement can model a correction without adding an implicit natural-language merge algorithm.
- Applications still decide how IDs are assigned, which claims are admitted, how long they live, and whether a replacement is semantically valid.
- Expiry is availability, not guaranteed physical deletion. Enterprise backends still own cleanup, legal deletion, retention, and audit.
- Confidence, semantic deduplication, contradiction resolution, embeddings, and deletion APIs remain outside this decision.

## Rejected Alternatives

- **Trust Adapters to filter expiry:** one faulty Adapter could inject stale evidence into model context.
- **Put Tenant/User types in Scope:** couples the embeddable runtime to Enterprise records.
- **Expose provenance IDs to the model:** increases context and leaks internal correlation data without improving structural validation.
- **Last-write-wins by default:** silently hides ID collisions and makes accidental overwrite hard to diagnose.
- **Semantic merge in Core:** requires model/domain policy and cannot provide a general correctness guarantee.
- **Treat expiry as deletion:** an availability timestamp does not prove physical erasure from backups or external stores.
