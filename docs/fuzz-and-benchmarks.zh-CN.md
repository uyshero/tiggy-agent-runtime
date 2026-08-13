# Fuzz 与 Benchmark

普通 `make verify` 会把所有 Fuzz Seed 当作单元测试运行。持续变异是可选的，
不会让常规 CI 失去确定性：

```bash
make fuzz FUZZ_TIME=30s
```

覆盖面包括共享严格 JSON 解码器、Orchestrator Brief、Artifact Tool Result
Envelope，以及 OpenAI 兼容 SSE/Tool Call 拼装。测试只断言不 Panic，且任何
被接受的值都必须满足既有 Validate 契约；不需要 Model、数据库、网络或
Enterprise 服务。

可用下面的命令运行可重复的本地分配与吞吐基线：

```bash
make benchmark
```

Orchestrator Benchmark 覆盖 10、100、1000 个任务的 Brief 解码与 Checkpoint
校验，以及 100 个任务的完整进程内顺序执行；同时包含既有 Observer 直接、异步、
批量和并行 Benchmark。结果受 Go 版本、操作系统、CPU 和机器负载影响，只是
诊断基线，不是 Release SLO。

Fuzz 变异发现的 Corpus 会写入 Go Test Cache。新增 Corpus 在提交前必须审查；
它是回归 Fixture，不是执行产物，也不是 Enterprise 持久化记录。
