# 工程标准

## 公开 API

- 公开最小且完整的 API，具体实现放入 `internal/`；
- 所有阻塞和 I/O API 将 `context.Context` 作为第一个参数；
- 不在 Struct 中保存调用方 Context；
- 不暴露 Enterprise ID、数据库 Row、OpenAPI DTO 和第三方 Client 类型；
- 优先使用明确 Struct 和强类型 Decision，不使用泛化 `map[string]any`；
- 只在明确的协议扩展边界使用 `json.RawMessage`；
- 文档说明 Slice、Map、Buffer、Stream 和资源所有权；
- 异步边界上的可变值必须复制或显式转移所有权。

## 错误

- 使用 `%w` 或 `Unwrap` 保留 Cause；
- 模型和用户可见错误只包含明确安全的信息；
- Provider Error 使用稳定 Class；
- 模型可修正的 Tool 参数和业务错误转换为 Tool Result；
- Runtime 状态损坏、非法协议转换和扩展契约违反必须终止 Run；
- Retry 必须有类型化 Retryable Decision、有限次数、取消支持和可观测事件；
- Authentication、Permission、Invalid Request 和确定性校验错误默认不重试。

## Event 和可观测性

- 每次 Run 有稳定 `run_id`，子操作有 ID 和 Parent 关联；
- Event Sequence 在一个 Run 内为正数且单调；
- Run 恰好产生 Completed、Failed、Canceled 中的一个；
- Start/End Event 在正常、错误和取消路径上成对；
- 不能丢弃终止事件、Runtime Error、最终 Tool Result、Compression Outcome 和 Artifact Offload Outcome；
- Delta 可以按明确的背压策略合并；
- 默认不记录凭证、完整 Prompt 和原始敏感工具参数；
- Metrics Label 保持低基数，禁止使用运行 ID 作为 Label。
- 当 Telemetry Failure 不能导致 Run 失败时，运行 Observer 必须使用显式 Best-effort Boundary；Audit 和 Recording 仍可使用严格投递；
- 默认 Log、Metric 和 Trace 排除原始 Model Delta、Tool Argument/Result、Message/Memory Content、Scope Key 和 Artifact Body；
- Run Recording 使用显式 Protocol Version、有界增量写入、Payload Sanitization、连续 Sequence、Terminal Completeness Check 和按 Run 独立的 Integrity Link；
- Recording Digest 不得表述为 Signature 或 Durability；Journal Ownership、Flush、Authentication、Retention 和 Deletion 必须显式；

## 并发与生命周期

- 每个 goroutine 都有 Owner 和终止条件；
- 每个网络 Client、Stream、Subprocess 和临时文件都有关闭路径；
- 取消传播到 Model、MCP、Tool、Memory 和 FileSystem；
- Channel 明确 Producer、Owner 和 Close 规则；
- Tool 声明并发类别，不安全工具串行执行；
- 并发 Event 可以交错，Context 投影保持模型调用顺序；
- CI 强制运行 `go test -race ./...`。

## Context

- 选择历史前先预留输出 Token；
- Token 统计包含 System、Tool Schema、Recall Memory、未完成 Tool 关系和近期消息；
- Token Count Source、Context Window、Safety Margin、Utilization 和精确 Overflow 都是显式 Plan 字段；
- 自定义 Plan 在 Provider Call 前校验；Overflow 不得静默继续调用 Model；
- Fallback Model 使用独立 Planner，因为 Window 和 Tokenizer 可能不同；
- Tool Call 和 Tool Result 是不可拆分压缩组；
- 未完成 Tool Call 不能被压缩；
- Compression 记录来源范围、前后 Token、策略版本和 Fallback；
- Compression 失败不能静默删除上下文；
- Context Length 错误默认最多触发一次受策略控制的压缩重试。

## Memory

- Context 与长期 Memory 使用不同接口和生命周期；
- 开启跨 Run Recall 前，Memory Record 必须有 Scope 和来源；
- Expiry 使用包含边界，并由 Agent 在模型投影前再次执行；Expiry 不等于物理删除；
- Observe Admit Item 必须标识当前 Run 和至少一个真实 Source Message；
- Recall/Observe 失败默认可降级且可观测；
- Cancellation、Panic、跨 Scope Recall、非法 Item 和边界违规不能静默降级；
- Recall Value 只进入 Model Request View；Observe 只接收已完成的当前 Turn；
- Scope 隔离、包含边界的过期、去重和精确 ID 冲突处理必须测试；物理删除仍是后续或 Enterprise Adapter 的要求；
- 未经 Policy 判断，Tool Error 和未验证模型声明不能作为事实存储；
- Memory Adapter 不在 Core API 暴露后端 Record 类型。
- Core Memory Contract 不暗示数据库、Vector Index 或 Durability 保证。
- 进程内 Memory 必须有界，无显式 Policy 时不 Admit，原子 Commit，且不静默覆盖或驱逐 Item。
- Exact-ID Conflict 行为必须显式；Core 不推断 Semantic Merge、Confidence 或 Contradiction Resolution。

## Artifact

- 大型 Tool Result 在模型投影前 Offload，并使用版本化、有完整性绑定的 Payload；
- Object Reference 是 opaque ID，不是 Signed URL、Credential 或后端 Record；
- Reference 记录 Media Type、Byte Size 和 SHA-256 Digest，并必须与存储 Payload 匹配；
- Store Durability、Authorization、Encryption、Retention、Retrieval 和 Garbage Collection 是明确的 Adapter 职责；
- Put 不自动重试，因为取消或 Transport Failure 后存储副作用可能有歧义；
- Offload Failure 可观测且 Fail-closed；Core 不能静默内联超过边界的结果；
- Core 不自动 Fetch Artifact Content；模型访问必须通过显式授权的 Retrieval Tool。

## Toolkit、MCP 和 Skills

- Native、MCP、Skill 和 Built-in Tool 共享统一 Tool Call/Result；
- Tool Name 在 Namespace 后唯一，并在模型调用前校验；
- Input Schema 必须是有效 JSON，并在执行前校验；
- Tool Error 区分模型可修正错误和 Runtime Error；
- Tool 输出、耗时和并发均有边界；
- MCP Client 明确 Connect、Health、Reconnect、Cancel 和 Close；
- MCP Discovery 和 Wire Message 在 Definition 或 Result 进入模型上下文前实施边界；
- MCP Transport 失败不得触发隐式 Tool Call 重放，在应用解决前外部效果存在歧义；
- MCP SDK 类型保持在 Adapter 内部，不得出现在 Core 公开签名中；
- Skill 是带版本、指令有界且显式激活工具/资源的能力单元；
- Skill Discovery 失败即整体失败；严格 Manifest 在发布 Catalog 前拒绝未知/重复字段和路径逃逸；
- Skill 和 Resource 选择在内容 I/O 前完整校验；激活内容不可变且携带可复现 Digest；
- Skill 内容具有 System 权限，必须来自应用已授权来源；
- Core 不自动下载、执行或让 Model 选择 Skill；外部格式必须通过显式 Adapter；
- Built-in 文件工具不能逃逸 SafeSpace Root。
- Built-in Tool 在输入校验后、产生外部效果前检查强类型 Permission Action；
- Built-in 文件 Tool 先规范化路径，再把同一个 Target 用于 Permission 和 I/O；
- 递归搜索必须有明确 Entry、Depth、Result、Output 和单文件上限；
- Shell 执行必须显式注入 Command Runner，Core 不安装默认本地执行器；
- Permission Request 和默认日志不得包含完整脚本，授权只使用规范化工作目录；
- 不得把本地进程 Root 宣称为对绝对路径、网络、子孙进程或其他 Host 资源的沙箱；
- Command Runner 必须响应取消，并限制脚本与捕获输出；取消不承诺回滚已产生的外部效果；
- Permission 拒绝可由模型处理，非法 Request 和 Policy 故障属于 Runtime Failure。

## 扩展安全

- Port 替换能力，Policy 作决策，Middleware 包裹操作，Observer 只读；
- Middleware 不获得无限制可变 Agent State；
- 扩展顺序确定且有文档；
- Plugin Panic 在边界 Recover 并转换为结构化 Runtime Failure；
- 可选扩展声明失败是可降级还是 Run Fatal；
- 扩展 Timeout 和资源所有权明确。

## 测试

行为变更按风险覆盖以下测试：

- 协议校验和深拷贝；
- Model/Tool 正常路径；
- 终止事件唯一性；
- 每个外部边界的取消；
- Timeout 和 Retry 上限；
- 非法 Model/MCP/Tool 响应；
- Context 压力和 Compression 失败；
- Memory 失败和 Scope 隔离；
- 并发 Tool 顺序和部分失败；
- 慢 Event Consumer 和背压；
- Panic 隔离和资源清理；
- Race Detection。

确定性 Fault Rule 使用精确 Call Ordinal。Cancellation Test 在 Boundary Entry 上同步，禁止依赖 Sleep。After-delegate Fault 必须断言 Side-effect Count 和无隐式 Replay，不能只检查返回 Error。

单元测试默认确定性执行，不依赖真实模型、数据库和网络。外部兼容测试必须显式开启并与单元 CI 分离。

## 兼容性

- `v1.0.0` 后公开 Go API 遵守语义化版本；
- `v1` 前允许 Breaking Change，但必须写入 Release Note；
- Message、Event 和未来 Snapshot 在持久化前引入协议版本；
- 新增 Event Type 是兼容变更，改变已有 Event 语义是 Breaking Change；
- Error Class 和 Terminal Reason 是兼容契约；
- Deprecation 必须给出替代项和移除版本。

公开契约 PR 必须说明动机、非目标、兼容影响、Event/Error 影响、取消与资源所有权、故障测试和文档更新；难以逆转的决策必须提交 ADR。
