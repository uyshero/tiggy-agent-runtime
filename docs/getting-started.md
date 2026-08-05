# Tiggy Agent Runtime Getting Started

## Scope

This guide connects the offline example to a real application integration. Tiggy Agent Runtime is an in-process Go library. It does not create model accounts, databases, MCP servers, telemetry backends, or workers.

The minimum configuration is:

```text
one model.Model + one agent.Config + one User Message
```

Toolkit, Memory, Context Planner, MCP, Skills, Observers, and Recorder are optional capabilities. Go 1.25.12 or later is currently required. The project is Apache-2.0 licensed, has a local `v0.1.0` release baseline, and remains pre-v1; neither the tag nor these docs claim production readiness.

## 1. Run the Offline Example

From the repository root:

```bash
make verify
go run ./examples/basic-agent
```

The example needs no credentials or network. It executes a complete User -> Model -> Tool -> Model -> Completed flow. See [examples/basic-agent/main.go](../examples/basic-agent/main.go). Replace its `demoModel` with a Provider Adapter implementing `model.Model` in an application.

## 2. Core Objects

| Object | Responsibility |
|---|---|
| `model.Model` | maps a provider-neutral Request to one concrete provider call and returns a normalized Response/Error |
| `tool.Tool` | declares a Tool Definition and executes one bounded operation |
| `toolkit.Toolkit` | registers Tools and creates an immutable snapshot for each Run |
| `agent.Agent` | executes the bounded ReAct loop, owns State, and emits Events |
| `message.Message` | represents System, User, Assistant, and Tool messages |
| `event.Sink` | synchronously consumes lifecycle Events for one Run |

The embedding application owns Provider credentials, database pools, MCP Clients, SafeSpaces, Telemetry Providers, and every other injected resource.

## 3. Implement a Provider Adapter

The Runtime calls this contract:

```go
type Model interface {
    Generate(
        context.Context,
        model.Request,
        model.DeltaSink,
    ) (model.Response, error)
}
```

For an OpenAI-compatible Chat Completions endpoint, use the optional standard-library Adapter:

```go
provider, err := openai.New(openai.Config{
    BaseURL: "https://provider.example/v1",
    APIKey:  os.Getenv("MODEL_API_KEY"),
    Client:  &http.Client{Timeout: 90 * time.Second},
})
```

See [OpenAI-compatible Provider Adapter](./provider-openai.md) for Tool mapping, streaming, parameters, ownership, and failure semantics. Other providers implement the same small `model.Model` contract.

A Provider Adapter should:

1. own its credentials and HTTP client;
2. map Messages, Tools, output limits, and Parameters to the provider wire format;
3. deliver stream deltas synchronously to the optional sink;
4. normalize the final Message, Stop Reason, and Usage;
5. return a typed `model.ProviderError` with a safe class and retry decision;
6. honor Context cancellation and close every per-call response or stream.

Structural skeleton:

```go
type Provider struct {
    client *http.Client
    token  string
}

func (p *Provider) Generate(
    ctx context.Context,
    request model.Request,
    sink model.DeltaSink,
) (model.Response, error) {
    wireRequest, err := toWireRequest(request)
    if err != nil {
        return model.Response{}, err
    }
    wireResponse, err := p.callProvider(ctx, wireRequest, sink)
    if err != nil {
        return model.Response{}, mapProviderError(err)
    }
    return fromWireResponse(wireResponse)
}
```

The skeleton is for custom Adapters. Core remains Provider-neutral: `agent` and `model` do not depend on the optional OpenAI-compatible package or a vendor SDK. `model.Identity` is diagnostic/routing metadata and must never contain credentials.

## 4. Register Tools

A native Tool implements `Definition` and `Execute`. The compiled clock Tool is in the basic example. Register Tools with:

```go
kit, err := toolkit.New(lookupTool{})
```

Use explicit namespace and capability groups where needed:

```go
err := kit.RegisterWith(toolkit.Registration{
    Tool:      anotherTool,
    Namespace: "crm",
    Groups:    []string{"read", "customer"},
})
```

Set `agent.Config.ToolGroups` to project only selected groups. Input is validated against JSON Schema before execution. Return `tool.Error` for safe model-actionable failures; ordinary errors, panics, and invalid Results terminate the Run as Tool Runtime failures.

## 5. Construct and Run an Agent

```go
runtime, err := agent.New(agent.Config{
    Name:          "support-agent",
    SystemPrompt:  "Use tools when evidence is required.",
    Model:         provider,
    ModelIdentity: model.Identity{Provider: "company", Model: "stable"},
    Toolkit:       kit,
    Limits:        agent.DefaultLimits(),
})
if err != nil {
    return err
}

input := message.Message{
    Role:       message.RoleUser,
    Visibility: message.VisibilityPublic,
    Content: []message.Content{{
        Type: message.ContentText,
        Text: "Look up the customer status.",
    }},
}
result, err := runtime.Run(ctx, input)
```

`agent.New` validates and copies configuration. Tighten the default iteration, Model Call, Tool Call, token, and duration limits for the application. One Agent can be reused concurrently only when every injected dependency obeys its concurrency contract.

## 6. Observe a Run

Use `RunStream` for logs, telemetry, recording, or a UI timeline:

```go
logSink, err := observe.LogSink(slog.Default(), observe.LogOptions{})
if err != nil {
    return err
}
sink := observe.BestEffort(logSink, func(err error) {
    fmt.Fprintf(os.Stderr, "observer error: %v\n", err)
})
result, err := runtime.RunStream(ctx, input, sink)
```

A direct Sink is strict: an error or panic fails the Run. Operational logging and telemetry normally use `BestEffort`; required audit/recording remains strict. Always inspect both `result` and `err`, because Event delivery can fail independently of the execution result.

Continue a complete conversation with:

```go
next, err := runtime.Continue(ctx, result.State, nextInput, sink)
```

`Result.State` is an in-process value, not a durable checkpoint or crash-recovery protocol.

## 7. Add Memory

```go
scope := memory.Scope{Namespace: "workspace", Key: "user-123"}
mem, err := memory.NewInMemory(memory.InMemoryOptions{
    InitialItems: []memory.Item{{
        ID:    "preference-language",
        Scope: scope,
        Text:  "The user prefers English responses.",
        Provenance: memory.Provenance{
            Source: "application-seed",
        },
    }},
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    // Model, ModelIdentity, Toolkit, Limits ...
    Memory:        mem,
    MemoryScope:   scope,
    MemoryOptions: memory.DefaultOptions(),
})
```

InMemory is bounded and process-local. Without an Admission policy, Observe stores nothing. Enterprise applications implement `memory.Port` while keeping database records, tenant IDs, embedding clients, and vector backends private to the Adapter.

## 8. Import MCP Tools

MCP is an optional external resource. Connect before constructing the Agent, register the discovered Tools, and close the caller-owned Client:

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

A nil stdio Environment is empty rather than inherited. MCP processes are not sandboxes, and transport failure never causes implicit Tool-call replay. See [MCP Client semantics](./mcp.md).

## 9. Activate Skills

Skills are explicitly selected trusted System Instruction resources; they do not execute code or acquire Tools automatically.

```go
space, err := safespace.OpenSafeSpace("./agent-assets", safespace.DefaultLimits())
if err != nil {
    return err
}
defer space.Close()

catalog, err := skill.Discover(ctx, space, "skills", skill.DefaultLimits())
if err != nil {
    return err
}
active, err := catalog.Activate(ctx, skill.ActivationRequest{
    Name: "customer-support", Resources: []string{"policy"},
})
if err != nil {
    return err
}

config.Skills = active
```

This assumes `./agent-assets/skills/<directory>/skill.json` exists. Every Skill Tool Group must already exist in Toolkit. See [Skill semantics](./skill.md).

## 10. Add Context Planning

Use a provider-aware Token Counter for real integrations:

```go
planner, err := contextmanager.NewTokenPlanner(
    contextmanager.TokenPlannerOptions{
        Counter:             providerCounter,
        CountSource:         contextmanager.CountSourceProvider,
        ContextWindowTokens: 128_000,
        SafetyMarginTokens:  1_024,
    },
)
if err != nil {
    return err
}
config.ContextPlanner = planner
config.ContextCompressor = compressor
config.FailurePolicy = agent.TransientModelFailurePolicy{
    EnableContextRecovery: true,
}
```

Core does not infer tokenizers or windows from model names. Overflow fails before the Provider call unless an application-owned Compressor returns a valid structurally safe summary decision. `EnableContextRecovery` separately permits one compression retry when the Provider reports Context Length despite a fitting preflight Plan. It is disabled by default, consumes `MaxModelCalls`, and never rewrites full State. See [Context Manager semantics](./contextmanager.md).

## 11. Add a Recorder

```go
journal, err := record.NewMemoryJournal(record.MemoryOptions{})
if err != nil {
    return err
}
recorder, err := record.New(journal, record.Options{})
if err != nil {
    return err
}
sink := observe.Chain(
    observe.BestEffort(logSink, reportObserverError),
    recorder.Sink,
)
result, err := runtime.RunStream(ctx, input, sink)
```

The Recorder is strict in this composition. MemoryJournal is process-local. JSONL durability, flush, close, rotation, encryption, and retention belong to the application. Recording supports diagnosis and integrity validation, not execution replay or signatures.

## 12. Test Deterministically

Run the compiled Model-Tool-Model test:

```bash
go test -v ./examples/agenttest-basic
```

Use `ScriptedModel`, deterministic IDs/time, `ValidateTrace`, and exact-call FaultInjector rules. Assert status and stable RunError codes, Model/side-effect call counts, the actual projected Requests from `provider.Calls()`, and terminal invariants.

See [Agent test kit](./agenttest.md) and [fault injection](./fault-injection.md).

## Recommended Adoption Order

```text
offline example
  -> real Provider Adapter
  -> one native Tool
  -> RunStream and structured logs
  -> agenttest failure paths
  -> Context Planner
  -> Memory or MCP
  -> Skills, Recorder, and OpenTelemetry
```

For each new boundary, define cancellation, timeout, Close ownership, error normalization, sensitive-data handling, and side-effect replay policy before enabling it.

Before deployment, tighten Limits, configure accurate token counting, test cross-scope Memory rejection, use appropriate best-effort/strict Observer boundaries, close all external resources, and run `make verify` plus `make vuln`. Run `make mcp-compat` when MCP interoperability is part of the deployment.

These checks establish an in-process reliability baseline. They do not imply process recovery, database durability, or exactly-once external effects.

## Further Reading

- [Architecture](./architecture.md)
- [Agent Runtime](./runtime.md)
- [Toolkit](./toolkit.md)
- [Observation](./observe.md)
- [Memory](./memory.md)
- [Context Manager](./contextmanager.md)
- [MCP Client](./mcp.md)
- [Skills](./skill.md)
- [Run Recorder](./record.md)
- [Agent test kit](./agenttest.md)
