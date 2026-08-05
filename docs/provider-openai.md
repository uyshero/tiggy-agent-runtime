# OpenAI-compatible Provider Adapter

## Boundary

`provider/openai` is an optional, standard-library Adapter implementing `model.Model` through the streaming Chat Completions protocol. Applications import and construct it explicitly. `agent`, `model`, and the other Core packages do not depend on it, and the Adapter does not read environment variables or global credentials.

It is not an OpenAPI client for Tiggy Manage Agent. It calls the configured model endpoint directly from the embedding Go process.

## Construction

The application owns the API key, `http.Client`, transport, connection pool, proxy, timeout, and shutdown:

```go
client := &http.Client{Timeout: 90 * time.Second}
provider, err := openai.New(openai.Config{
    BaseURL: "https://provider.example/v1",
    APIKey:  os.Getenv("MODEL_API_KEY"),
    Client:  client,
})
if err != nil {
    return err
}

runtime, err := agent.New(agent.Config{
    Name:  "support-agent",
    Model: provider,
    ModelIdentity: model.Identity{
        Provider: "openai-compatible",
        Model:    "configured-model",
    },
    Toolkit: toolkit,
})
```

HTTPS is required by default. `AllowHTTP` exists only for a trusted local or private endpoint. `New` validates configuration without making a request, and neither owns nor closes the injected Client.

## Supported Mapping

The Adapter currently maps:

- text System, User, and Assistant Messages;
- Assistant Tool Calls and text Tool Results;
- Runtime Tool Definitions to OpenAI function tools;
- streamed text, reasoning, Tool Call fragments, finish reason, and Usage;
- `x-request-id` or `request-id` into successful `Response.ProviderRequestID` and failed `ProviderError.RequestID` values;
- provider-neutral `Request.Parameters` into top-level wire fields.

`Request.Parameters` must be a JSON object and cannot override Runtime-owned `model`, `messages`, `tools`, `max_tokens`, `max_completion_tokens`, `n`, `stream`, or `stream_options`. This prevents Provider-specific aliases and multiple-choice settings from bypassing the Runtime output and single-response contract. Images, Artifact blocks, non-text Tool Results, legacy non-streaming completions, and provider-specific reasoning replay are not supported. Portable Assistant reasoning blocks are omitted when history is sent again.

Tool Call argument fragments are assembled by index. Invalid final JSON becomes safe `{}` plus `ToolCall.ArgumentsError`, allowing Toolkit validation to produce a model-actionable error instead of executing malformed arguments.

## Failure and Streaming Semantics

HTTP and stream failures become `model.ProviderError` values with stable classes for authentication, permission, quota, rate limit, context length, timeout, invalid request, unavailable model, safety filtering, server, network, cancellation, and stream protocol failures. For non-success HTTP responses, the Adapter reads at most 4 KiB and recognizes an explicit allowlist of structured `error.code`/`error.type` values; it never extracts or retains the Provider message. Unknown, oversized, or malformed HTTP error envelopes fall back to HTTP-status classification, while unknown SSE error metadata uses the safe `stream_error` code. Invalid Tool Call indices and types, conflicting finish reasons, and safety-filter termination fail closed.

The Adapter performs no retry. A retry or Fallback requires the Agent's explicit bounded `ModelFailurePolicy`. It returns `DeltaSink` errors unchanged and stops reading, closes every response body before return, and uses the Run Context for cancellation. One SSE line is limited to 1 MiB; Runtime limits separately bound retained Delta count and bytes.

Use `make eval-provider-reliability` for deterministic HTTP/SSE failure coverage. `make provider-live` is the explicit, potentially billed external smoke check described in [Live Provider contract verification](./live-provider.md).
