# Run Recorder 语义

## 目标与边界

`record` 是 Runtime 的黑匣子 Journal。它把同步 Version 1 Event Stream 转换为带版本和完整性链接的 Entry，供 Run 结束后检查与校验。它拿不到 Agent State、Model Request、Tool 实现、数据库 Identity、Tenant Record 或 Replay 权限。

Recording 不等于执行 Replay。本包只支持 Inspect 和结构校验，绝不调用 Model、Tool、Memory Port 或其他外部效果。Re-execute 必须由独立产品 Policy 处理 Credential、Cost、Non-determinism、Permission 和之前可能已发生但结果不明确的副作用。

应用在一个 `Journal` 上创建可并发复用的 `Recorder`，再把 `Recorder.Sink` 传给 `RunStream`/`Continue`，也可与其他 Observer 组合。直接 Recorder 是严格模式：Validation、Sanitization 或 Journal Failure 按 Event Sink Failure 处理。使用 `observe.BestEffort` 表示显式选择运行可用性而放弃完整记录；当 Recording 是 Audit Requirement 时不能这样配置。

## Recording Protocol

每个 `Entry` 使用 `tiggy.runtime.record.v1`，包含：

- Run ID 与独立副本的 Version 1 Event；
- Payload 是否被脱敏；
- 同一 Run 上一个 Entry 的 Digest；
- 覆盖其他全部 Entry 字段的 SHA-256 Digest。

首个 Entry 必须是 Sequence 1 且没有 Parent 的 `run.started`。Sequence 必须连续，Event ID 必须唯一，每个 Parent ID 必须引用同一 Run 中更早的 Event。Terminal Event 关闭 Recording，且必须是最后一个 Event。

`Recording.Validate` 接受进程中断留下的结构有效 Partial Journal；`ValidateComplete` 还要求存在 Terminal Event。校验可以发现内容变化、链路断裂/重排/缺失、重复 ID、非法 Parent 和 Terminal 后 Event。

Digest Chain 是完整性 Checksum，不是身份认证或不可否认性。能重写 Journal 的人也能重新计算全部 Digest。需要 Tamper Evidence 的 Enterprise Audit Storage 必须增加应用拥有的 Signature/MAC、受保护 Key Management、Immutable Storage Policy 和 Trusted Timestamp。

## 隐私

默认使用 `SafeSanitizer`。它保留已审查的 Lifecycle Payload，但移除 Model Delta Text、Reasoning、Tool-call Fragment 和 Provider Error Body；Tool Delta 和未知未来 Event Type 的完整 Payload 在完成隐私审查前都会移除。任何变化都会设置 `payload_redacted=true`。

`FullSanitizer` 必须显式选择，会保留所有 Payload Byte。调用方随后负责 Classification、User Consent、Encryption、Access Control、Retention、Deletion 和 Incident Exposure。自定义 Sanitizer 只能修改 Payload；修改 Event Protocol、ID、Type、Sequence、Parent、Run ID 或 Timestamp 会被 Recorder 拒绝。Sanitizer Error/Panic 会转换为严格 Sink Error。

## 边界与并发

Recorder 默认最多同时跟踪 1,024 个 Active Run、每 Run 100,000 个 Event、每个脱敏后 Event 1 MiB。同一个 Recorder 可供多个 Agent Run 并发复用。它串行执行 Validation、Digest 和 Journal Append，因此 Journal 看到确定的已接受顺序。Event Stream 仍是同步的；阻塞 Journal 会阻塞 Run Callback，Core 无法抢占。

## Journal

`MemoryJournal` 按总 Entry 数和编码 Byte 设置上限，返回独立 Snapshot，从不驱逐，并在进程退出时丢失全部数据。它只用于测试与本地诊断。

`JSONLJournal` 每行写一个 Entry，允许并发 Run 的 Entry 交错；Digest Chain 仍按 Run 独立。调用方拥有 Writer 及全部 File Permission、Buffering、Flush/fsync、Close、Rotation、Encryption、Retention 和 Recovery。Writer Error 可能留下不完整的最后一行；成功 `Append` 本身不证明磁盘 Durability。

`ReadJSONL` 限制 Line Size、总 Entry 和 Recording 数，按 Run 聚合交错 Entry，并校验每条 Chain。默认返回用于 Crash Diagnostics 的 Incomplete Recording；启用 `RequireComplete` 后拒绝它们。

Journal Append 是外部副作用。如果自定义 Journal 在部分接受 Entry 后返回 Error，Core 无法判断写入是否已 Commit，也不会自动 Retry。

## 非目标

Core 不提供 PostgreSQL Schema、Object Store Adapter、Index、Query API、Tenant Authorization、Retention Job、Legal Deletion、Signature、跨进程锁、Durable Queue、Remote Shipping、UI Timeline 或 Model/Tool Re-execution。Enterprise 可以实现 `Journal` 或摄取 JSONL，同时保持 Backend Type 和 Policy 不进入 Core。
