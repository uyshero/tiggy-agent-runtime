# Tool Approval and Resume Semantics

## Scope

The `approval` package and `agent.Resume` provide an in-process execution
protocol for stopping before a Tool side effect and continuing after an
embedding application supplies a human decision. Core does not identify the
approver, persist decisions, send notifications, or wake distributed Workers.

Approval and built-in Tool permission are separate layers:

- `approval.Policy` gates the complete model-produced Tool batch before any
  Tool in that batch executes;
- `permission.Policy` remains a Tool-owned resource authorization check;
- the embedding application owns UI, identity, storage, expiry, and delivery.

A nil `ApprovalPolicy` allows every call for backward compatibility.

## Configuration

```go
gate := approval.PolicyFunc(func(
    ctx context.Context,
    request approval.Request,
) (approval.Decision, error) {
    if request.Tool == "builtin.edit" {
        return approval.Decision{
            Behavior: approval.BehaviorRequire,
            Reason:   "Editing a workspace file requires review.",
        }, nil
    }
    return approval.Decision{Behavior: approval.BehaviorAllow}, nil
})

runtime, err := agent.New(agent.Config{
    Name:           "assistant",
    Model:          provider,
    ModelIdentity:  identity,
    Toolkit:        kit,
    ApprovalPolicy: gate,
})
```

`Request.Arguments` contains owned exact JSON bytes so a Policy can classify
the effect. Policies must not put credentials or raw arguments in `Reason`;
Reason is operator- and model-visible.

## Suspend and Resume

When any call in one model-produced Tool batch requires approval, no Tool in
that batch executes. `Run` returns `StatusSuspended`, a State containing the
pending Tool Calls, and a `Suspension` containing one frozen disposition per
call. Only required calls have an `ApprovalID`.

```go
result, err := runtime.Run(ctx, input)
if err != nil {
    return err
}
if result.Status == agent.StatusSuspended {
    // The application may serialize result.State while collecting decisions.
    decisions := []approval.Resolution{{
        ApprovalID: result.Suspension.Calls[0].ApprovalID,
        Status:     approval.ResolutionApproved,
    }}
    result, err = runtime.Resume(ctx, result.State, decisions, sink)
}
```

`Resume` requires exactly one Resolution for every required Approval. Missing,
duplicate, or unknown decisions return an error while preserving the suspended
State so the caller can retry. Approval wait time is outside the original Run
Context and consumes no Runtime goroutine.

Approved calls execute in original model order. Rejected calls do not execute;
they receive a non-retryable `approval_rejected` Tool Result, after which the
model can continue. Calls denied directly by Policy receive `approval_denied`.
The ApprovalPolicy is not called again during Resume.

## State Machine View

`StatusSuspended` does not mean a goroutine is parked, and it is not task
failure. It means the current Runtime invocation has terminated safely and
returned a verifiable continuation point.

```text
Run / Continue
  -> model emits one Tool batch
  -> ApprovalPolicy classifies every call
     -> all allow/deny: execute batch and continue loop
     -> any require: store Suspension, emit approval.required, return run.suspended

Resume
  -> validate suspended State
  -> validate exact approval resolutions
  -> validate current Toolkit definition digests
     -> mismatch or bad resolution: keep same Suspension and return suspended
     -> valid: clear Suspension, emit approval.resolved, execute original batch
```

This state machine has three key invariants:

- no Tool in the batch may execute before suspension, otherwise approval
  protects only part of the side effect boundary;
- Resume must validate State, Resolution, and Toolkit contracts, otherwise the
  approved operation may not be the original call;
- bad resolutions or Toolkit mismatches must preserve the original Suspension,
  otherwise the caller cannot correct the input and retry safely.

Therefore `run.suspended` is terminal for one invocation: that invocation will
not continue executing in the background. It is not terminal for the business
task: the application may persist `Result.State`, collect human decisions, and
start a later `Resume` invocation.

## Integrity and Events

Suspension binds each call to:

- Call ID and Tool name;
- SHA-256 of Tool name plus exact argument JSON bytes;
- SHA-256 of the frozen model-visible Tool definition;
- the original current-turn boundary.

Mutation of State arguments or resuming against a different Tool contract
fails before Tool execution. Core cannot prove that mutable internals behind a
Tool implementation remained unchanged; adapters must keep their own execution
contracts stable.

A Toolkit contract mismatch returns `approval_toolkit_mismatch` while
preserving the suspended State. No approval is marked resolved and no Memory
or Tool operation starts, so the application can retry with a compatible
Runtime. Mutating the suspended State itself is rejected as invalid input.

The first invocation emits `approval.required` and terminates with
`run.suspended`. Resume emits a new `run.started` carrying `continuation_id`,
then `approval.resolved`, Tool events, and one terminal Run event. Approval
events contain hashes and bounded reasons, never raw Tool arguments.

Memory Recall runs again on Resume because recalled evidence is a request-only
view and is not persisted in Agent State. A suspended turn is observed by
Memory only after it eventually completes.

## Persistence Boundary

`State` and `Suspension` are JSON-serializable continuation values, not a
durable scheduler. Applications that persist them must provide atomic storage,
authorization, encryption, retention, expiry, single-resumer coordination, and
Worker wake-up. Core still does not promise crash recovery or exactly-once
external Tool effects.
