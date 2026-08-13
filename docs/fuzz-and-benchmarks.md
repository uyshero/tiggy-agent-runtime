# Fuzzing and Benchmarks

The normal `make verify` run executes every fuzz seed as a unit test. Continuous
mutation is opt-in so ordinary CI remains deterministic and fast:

```bash
make fuzz FUZZ_TIME=30s
```

The targets cover the shared strict JSON decoder, Orchestrator Brief decoding,
Artifact Tool Result envelopes, and OpenAI-compatible SSE/Tool Call assembly.
They assert panic freedom and require every accepted value to satisfy its
existing validation contract. They use no model, database, network, or
Enterprise service.

Run the repeatable local allocation and throughput baselines with:

```bash
make benchmark
```

Orchestrator benchmarks cover 10, 100, and 1000-task Brief decoding and
checkpoint validation, plus complete 100-task in-process sequential execution.
The command also includes the existing direct, asynchronous, batched, and
parallel Observer benchmarks. Results depend on the Go version, operating
system, CPU, and machine load; they are diagnostic baselines, not release SLOs.

Fuzz corpus files are generated under Go's test cache when a mutation finds an
interesting input. Review any new corpus before committing it; a corpus is a
regression fixture, not an execution artifact or an Enterprise persistence
record.
