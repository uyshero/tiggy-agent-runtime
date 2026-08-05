# Live Provider Contract Verification

The live Provider check proves that a real OpenAI-compatible endpoint can satisfy Tiggy's `model.Model` contract through the complete Agent Runtime and the optional `provider/openai` Adapter. It is an opt-in integration test, not normal CI traffic.

## Boundary

The harness lives behind the `liveprovider` build tag and reuses the standard-library-only `provider/openai` Adapter. It does not import `tiggy-manage-agent`, add a module dependency, start TMA Server or a database, or place Provider-specific types in Core APIs.

Normal `make verify` forces `TIGGY_RUN_LIVE_PROVIDER_TESTS=0`. It compiles the harness and tests the SSE/dotenv parsers and deterministic Provider failure fixtures without opening an external network connection. A real call requires both the build tag and the explicit runtime switch used by `make provider-live`.

## Reliability Fixtures

Run the Provider reliability layer alone with:

```bash
make eval-provider-reliability
```

An injected `http.RoundTripper` verifies the complete Adapter-to-Runtime path for a successful stream, HTTP authentication, rate-limit, quota, context-length, and unavailable failures, malformed SSE, an interrupted stream read, a clean EOF without a finish reason, safety filtering, request deadline cancellation, and Runtime Delta backpressure. Every scenario requires a valid final State and lifecycle trace and exactly one HTTP exchange, proving that the fail-fast configuration does not replay a failed request implicitly.

These fixtures use no Provider credentials, external socket, sleep-based timing, or billed request. The deadline case waits only for the Runtime-owned Context to expire. The suite validates Adapter classification and Runtime containment; it does not prove that a specific external Provider has a given availability or latency.

## TMA Configuration

By default the command reads the adjacent repository's `.env`:

```text
../tiggy-manage-agent/.env
```

It recognizes:

```text
TMA_LLM_PROVIDER
TMA_LLM_PROVIDER_TYPE
TMA_LLM_MODEL
TMA_LLM_BASE_URL
TMA_LLM_API_KEY_ENV
TMA_LLM_API_KEY
```

Shell environment values override dotenv values. The dotenv file is parsed as data and is never sourced or executed. The API key is used only in the upstream Authorization header and is never printed, copied into Runtime Message/Event values, or written into the repository.

## Run

The command makes one billed text-generation request:

```bash
make provider-live
```

Use a different dotenv explicitly:

```bash
make provider-live TMA_ENV_FILE=/absolute/path/to/.env
```

Plain HTTP is rejected. A trusted local fixture may opt in with `TIGGY_LIVE_PROVIDER_ALLOW_HTTP=1`. Override the default 90-second deadline with a positive Go duration in `TIGGY_LIVE_PROVIDER_TIMEOUT`.

## Assertions

The check requires:

- the real endpoint to accept the exact provider-neutral Runtime request projection;
- streamed text to produce `model.call.delta` Events;
- the final response to contain the requested fixed marker and no extra text;
- Provider Usage to contain positive input and output token counts;
- the Agent to finish with `StatusCompleted`;
- `agenttest.ValidateTrace` to accept event sequence, parent links, operation pairing, and the unique terminal Event.

Successful output includes only Provider/model identifiers, Delta count, token counts, and elapsed time. It does not print the endpoint, credential, prompt, raw response, or generated text.
