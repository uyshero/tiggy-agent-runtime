package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/approval"
	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

// Status 是单次 Run 的终态分类。
type Status string

const (
	StatusCompleted Status = "completed" // 正常结束，有最终 Assistant 消息
	StatusFailed    Status = "failed"    // 可分类失败（模型/工具/限额/Sink 等）
	StatusCanceled  Status = "canceled"  // Context 取消或超时
	StatusSuspended Status = "suspended" // 等待人工审批后续跑（Resume）
)

// Result 汇总一次 Run 的终态、State、事件与可选挂起信息。
type Result struct {
	Status     Status               `json:"status"`
	State      State                `json:"state"`                // 可序列化对话快照
	Final      *message.Message     `json:"final,omitempty"`      // 成功时的最终 Assistant 消息
	Iterations int                  `json:"iterations"`           // 实际进入的模型迭代次数
	Events     []event.Event        `json:"events"`               // 本 Run 已发出事件的副本
	Suspension *approval.Suspension `json:"suspension,omitempty"` // StatusSuspended 时的续跑句柄
}

// Run 从空 State 启动一次同步 Run（无事件 Sink）。
func (a *Agent) Run(ctx context.Context, input message.Message) (Result, error) {
	return a.run(ctx, State{}, &input, nil, nil)
}

// RunStream 与 Run 相同，但按序把生命周期事件同步投递给 sink。
func (a *Agent) RunStream(ctx context.Context, input message.Message, sink event.Sink) (Result, error) {
	return a.run(ctx, State{}, &input, nil, sink)
}

// Continue 在已有 State 上追加用户输入并继续 Run（非审批恢复路径）。
func (a *Agent) Continue(ctx context.Context, previous State, input message.Message, sink event.Sink) (Result, error) {
	return a.run(ctx, previous, &input, nil, sink)
}

// Resume 解析挂起 State 中的全部审批，并继续执行原 pending Tool 批次；
// 不再询问模型或 ApprovalPolicy。
func (a *Agent) Resume(ctx context.Context, previous State, resolutions []approval.Resolution, sink event.Sink) (Result, error) {
	return a.run(ctx, previous, nil, resolutions, sink)
}

// run 是 ReAct 主循环：回忆 → 迭代（上下文/模型/工具）→ 观察 → 终态事件。
//
// 入参约定：
//   - input != nil：Continue/Run 路径，追加 User 消息；resolutions 必须为空且无挂起
//   - input == nil：Resume 路径，用 resolutions 填回挂起的 Tool 批次
func (a *Agent) run(parent context.Context, previous State, input *message.Message, resolutions []approval.Resolution, sink event.Sink) (result Result, runErr error) {
	// 墙钟上限独立于调用方 Context；两者任一取消都会打断循环。
	ctx, cancel := context.WithTimeout(parent, a.config.Limits.MaxDuration)
	defer cancel()

	runID := a.config.IDs("run")
	emit := &emitter{runID: runID, ids: a.config.IDs, now: a.config.Now, sink: sink}
	transitions := &transitionEmitter{hook: a.config.TransitionHook, runID: runID, ids: a.config.IDs, now: a.config.Now}
	state := previous.Clone() // 不修改调用方持有的 previous
	view, viewErr := a.toolView()
	modelCalls := 0                         // 累计模型调用次数（含重试）
	toolCalls := 0                          // 累计工具调用次数
	iterations := 0                         // ReAct 迭代（每轮一次模型调用）
	startedTools := make(map[string]string) // callID -> ToolCallStarted 事件 ID，用于收尾配对
	continuationID := ""
	if previous.Suspension != nil {
		continuationID = previous.Suspension.ID // Resume 时把上一挂起 ID 写入 run.started
	}

	_, _ = emit.emit(event.RunStarted, "", a.runStartedPayload(continuationID))

	// finish 是唯一收口：校验 State、补齐未完成工具、发终态事件、返回 Result。
	finish := func(status Status, reason string, err *RunError, final *message.Message) (Result, error) {
		// Sink 若已失败，优先把终态改成 event_sink_failed（除非 err 已是 Sink 类）。
		if emit.sinkErr != nil && (err == nil || err.Class != ErrorEventSink) {
			status = StatusFailed
			reason = "event_sink_failed"
			err = &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}
			final = nil
		}
		if status == StatusSuspended {
			// 挂起态必须能被 Resume；结构非法则降级为失败。
			if stateErr := state.ValidateSuspended(); stateErr != nil {
				status = StatusFailed
				reason = "invalid_suspended_state"
				err = &RunError{Class: ErrorInternal, Code: "invalid_suspended_state", SafeMessage: "The Agent produced an invalid approval continuation.", Cause: stateErr}
				final = nil
			}
		}
		if status != StatusSuspended {
			// 非挂起收口：给未完成的 ToolCall 补 interrupted 结果，并清掉 Suspension。
			if len(state.PendingToolCalls()) > 0 {
				a.closePendingTools(&state, emit, startedTools, "interrupted", "Tool execution was interrupted before a result was produced.")
			}
			state.Suspension = nil
			if stateErr := state.ValidateComplete(); stateErr != nil {
				status = StatusFailed
				reason = "invalid_final_state"
				err = &RunError{Class: ErrorInternal, Code: "invalid_final_state", SafeMessage: "The Agent produced an invalid final state.", Cause: stateErr}
				final = nil
			}
		}
		var payloadError *event.ErrorPayload
		if err != nil {
			payloadError = eventError(err)
			_, _ = emit.emit(event.RuntimeError, "", payloadError)
		}
		// 消费 RuntimeError 时 Sink 仍可能失败；此时尚未发终态事件，应升为终态失败。
		if emit.sinkErr != nil && (err == nil || err.Class != ErrorEventSink) {
			status = StatusFailed
			reason = "event_sink_failed"
			err = &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}
			payloadError = eventError(err)
			final = nil
		}
		// 按 Status 选择终态事件类型（协议要求恰好一个终态）。
		terminalType := event.RunCompleted
		if status == StatusFailed {
			terminalType = event.RunFailed
		} else if status == StatusCanceled {
			terminalType = event.RunCanceled
		} else if status == StatusSuspended {
			terminalType = event.RunSuspended
		}
		suspensionID := ""
		if state.Suspension != nil {
			suspensionID = state.Suspension.ID
		}
		_, terminalEmitErr := emit.emit(terminalType, "", event.RunTerminalPayload{
			Status: string(status), Reason: reason, Iterations: iterations, Usage: state.Usage, Error: payloadError, SuspensionID: suspensionID,
		})
		if terminalEmitErr != nil {
			// 终态事件投递失败：执行结果仍返回，但 error 标明终端事件未送达。
			err = &RunError{
				Class: ErrorEventSink, Code: "terminal_event_failed", SafeMessage: "The terminal event could not be delivered.",
				Cause: errors.Join(err, terminalEmitErr),
			}
		}
		result := Result{
			Status: status, State: state.Clone(), Final: cloneMessagePointer(final), Iterations: iterations, Events: cloneEvents(emit.events),
		}
		if state.Suspension != nil {
			value := state.Suspension.Clone()
			result.Suspension = &value
		}
		if err == nil {
			return result, nil
		}
		return result, err
	}

	// ---- 启动前检查 ----
	if emit.sinkErr != nil {
		return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "run_start_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}, nil)
	}
	if viewErr != nil {
		return finish(StatusFailed, "toolkit_snapshot_failed", &RunError{
			Class: ErrorInternal, Code: "toolkit_snapshot_failed", SafeMessage: "The Agent could not create its Tool snapshot.", Cause: viewErr,
		}, nil)
	}
	if err := a.checkUsage(state.Usage); err != nil {
		return finish(StatusFailed, "usage_limit", err, nil)
	}

	turnStart := 0 // 本轮 User 消息在 Messages 中的下标，供 Memory Observe / 挂起续跑定位
	var recallInput message.Message
	var resumedCalls []message.ToolCall
	var resumedPlan []approval.PendingCall

	if input != nil {
		// ---- Continue / Run：追加 User 输入 ----
		if len(resolutions) != 0 || state.Suspension != nil {
			// 挂起态必须走 Resume，不能一边 Continue 一边带 resolutions。
			return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_state", SafeMessage: "Continue requires a complete Agent state; use Resume for a suspended state."}, nil)
		}
		if err := state.ValidateComplete(); err != nil {
			return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_state", SafeMessage: "The previous Agent state is invalid.", Cause: err}, nil)
		}
		normalized := a.normalizeInput(*input)
		if err := normalized.Validate(); err != nil || normalized.Role != message.RoleUser {
			if err == nil {
				err = fmt.Errorf("input role must be user")
			}
			return finish(StatusFailed, "invalid_input", &RunError{Class: ErrorInvalidInput, Code: "invalid_message", SafeMessage: "The input message is invalid.", Cause: err}, nil)
		}
		nextState := state.Clone()
		// 首轮且配置了 SystemPrompt/Skill 指令时，先插入内部 System 消息。
		if prompt, metadata, err := a.systemPrompt(); err != nil {
			return finish(StatusFailed, "invalid_instruction_stack", &RunError{Class: ErrorInternal, Code: "invalid_instruction_stack", SafeMessage: "The Agent could not assemble its instruction stack.", Cause: err}, nil)
		} else if len(nextState.Messages) == 0 && prompt != "" {
			nextState.Messages = append(nextState.Messages, message.Message{
				ID: a.config.IDs("msg"), Role: message.RoleSystem, Visibility: message.VisibilityInternal,
				Content: []message.Content{{Type: message.ContentText, Text: prompt}}, Metadata: metadata,
			})
		}
		turnStart = len(nextState.Messages) // User 消息即将写入的位置
		nextState.Messages = append(nextState.Messages, message.Clone(normalized))
		if err := nextState.Validate(); err != nil {
			return finish(StatusFailed, "invalid_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_state", SafeMessage: "The Agent input conflicts with the previous state.", Cause: err}, nil)
		}
		if commitErr := a.commitStateTransition(ctx, transitions, &state, nextState, "input_appended", ""); commitErr != nil {
			return finish(StatusFailed, "transition_failed", commitErr, nil)
		}
		recallInput = normalized
	} else {
		// ---- Resume：恢复挂起的 Tool 批次 ----
		if err := state.ValidateSuspended(); err != nil {
			return finish(StatusFailed, "invalid_suspended_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_suspended_state", SafeMessage: "The approval continuation is invalid.", Cause: err}, nil)
		}
		var resolveErr error
		resumedCalls = state.PendingToolCalls()
		resumedPlan, resolveErr = resolveApprovalPlan(*state.Suspension, resolutions)
		if resolveErr != nil {
			// 决议不匹配：保持挂起，让调用方改正后重试 Resume。
			return finish(StatusSuspended, "invalid_approval_resolution", &RunError{Class: ErrorInvalidInput, Code: "invalid_approval_resolution", SafeMessage: "The approval decisions do not match the pending continuation.", Cause: resolveErr}, nil)
		}
		if bindingErr := validateToolPlanBindings(view, resumedCalls, resumedPlan); bindingErr != nil {
			// Toolkit 定义或参数摘要变了：拒绝执行，仍保持挂起。
			return finish(StatusSuspended, "approval_toolkit_mismatch", &RunError{Class: ErrorInvalidInput, Code: "approval_toolkit_mismatch", SafeMessage: "The current Toolkit does not match the suspended approval continuation.", Cause: bindingErr}, nil)
		}
		resumeBindings, bindingErr := a.transitionToolBindings(state, view, resumedCalls)
		if bindingErr == nil {
			bindingErr = validateApprovalTransitionBindings(resumedPlan, resumeBindings)
		}
		if bindingErr != nil {
			return finish(StatusSuspended, "approval_toolkit_mismatch", &RunError{Class: ErrorInvalidInput, Code: "approval_toolkit_mismatch", SafeMessage: "The current Tool effect binding does not match the suspended approval continuation.", Cause: bindingErr}, nil)
		}
		continuationID := state.Suspension.ID
		turnStart = state.Suspension.TurnStart
		if turnStart >= len(state.Messages) || state.Messages[turnStart].Role != message.RoleUser {
			return finish(StatusFailed, "invalid_suspended_state", &RunError{Class: ErrorInvalidInput, Code: "invalid_suspended_state", SafeMessage: "The approval continuation does not identify its original user turn."}, nil)
		}
		recallInput = message.Clone(state.Messages[turnStart])
		// 对原先 Require 的条目发出 ApprovalResolved（approved/rejected）。
		for index, pending := range state.Suspension.Calls {
			if pending.Behavior != approval.BehaviorRequire {
				continue
			}
			resolved := resumedPlan[index]
			status := "approved"
			if resolved.Behavior == approval.BehaviorDeny {
				status = "rejected"
			}
			_, _ = emit.emit(event.ApprovalResolved, "", event.ApprovalPayload{
				ApprovalID: pending.ApprovalID, SuspensionID: state.Suspension.ID, CallID: pending.CallID,
				Tool: pending.Tool, ArgumentsDigest: pending.ArgumentsDigest, ToolDigest: pending.ToolDigest,
				Status: status, Reason: resolved.Reason,
			})
		}
		if emit.sinkErr != nil {
			return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed before approved Tools executed.", Cause: emit.sinkErr}, nil)
		}
		// 只有持久化 continuation consumed 成功后才清除挂起标记。
		if consumeErr := a.consumeContinuation(ctx, transitions, &state, continuationID); consumeErr != nil {
			return finish(StatusSuspended, "transition_failed", consumeErr, nil)
		}
	}

	// 按本轮 User 输入做 Memory 回忆；失败策略由 MemoryOptions.RecallFailureMode 决定。
	recall := a.recallMemory(ctx, runID, recallInput, emit)
	if recall.err != nil {
		if recall.canceled {
			return finish(StatusCanceled, recall.reason, recall.err, nil)
		}
		return finish(StatusFailed, recall.reason, recall.err, nil)
	}
	// Resume 路径：先执行已决议的工具批次，再进入正常模型循环。
	if len(resumedCalls) > 0 {
		bindings, batchDigest, freezeErr := a.freezeToolBatch(ctx, transitions, state, view, resumedCalls)
		if freezeErr != nil {
			return finish(StatusFailed, "transition_failed", freezeErr, nil)
		}
		stop := a.executeToolBatch(ctx, runID, &state, view, resumedCalls, resumedPlan, bindings, batchDigest, transitions, emit, startedTools, &toolCalls)
		if stop != nil {
			return finish(stop.status, stop.reason, stop.err, nil)
		}
	}

	// ---- ReAct 主循环：模型 →（可选）工具 → 再模型 ----
	for iterations < a.config.Limits.MaxIterations {
		if err := ctx.Err(); err != nil {
			return finish(StatusCanceled, "context_canceled", &RunError{Class: ErrorCanceled, Code: "context_canceled", SafeMessage: "The Agent run was canceled.", Cause: err}, nil)
		}
		if emit.sinkErr != nil {
			return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}, nil)
		}
		iterations++
		// 回忆投影只注入模型请求，不写进持久 State（避免污染对话账本）。
		modelMessages := projectRecalledMemory(state.Messages, recall.projection)
		generation := a.generateModel(ctx, runID, state, modelMessages, view.Definitions(), transitions, emit, &modelCalls)
		if generation.err != nil {
			if generation.canceled {
				return finish(StatusCanceled, generation.reason, generation.err, nil)
			}
			return finish(StatusFailed, generation.reason, generation.err, nil)
		}
		response := generation.response
		response = a.normalizeResponse(response)
		if err := response.Validate(); err != nil {
			runtimeError := &RunError{Class: ErrorModel, Code: "invalid_model_response", SafeMessage: "The model returned an invalid response.", Cause: err}
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "failed", Error: eventError(runtimeError),
			})
			return finish(StatusFailed, "invalid_model_response", runtimeError, nil)
		}
		nextState := state.Clone()
		nextState.Usage = nextState.Usage.Add(response.Usage)
		nextState.Messages = append(nextState.Messages, message.Clone(response.Message))
		if err := nextState.Validate(); err != nil {
			// Usage 描述已完成的供应商调用，即使响应无法提交到对话 State 也要记账。
			state.Usage = nextState.Usage
			runtimeError := &RunError{Class: ErrorModel, Code: "invalid_model_response", SafeMessage: "The model response conflicts with the Agent state.", Cause: err}
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "failed", Error: eventError(runtimeError),
			})
			return finish(StatusFailed, "invalid_model_response", runtimeError, nil)
		}
		if commitErr := a.commitStateTransition(ctx, transitions, &state, nextState, "model_response_committed", generation.transitionID); commitErr != nil {
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "failed", Error: eventError(commitErr),
			})
			return finish(StatusFailed, "transition_failed", commitErr, nil)
		}
		if err := a.checkUsage(state.Usage); err != nil {
			_, _ = emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
				CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "completed",
				StopReason: string(response.StopReason), Usage: response.Usage,
			})
			return finish(StatusFailed, "usage_limit", err, nil)
		}
		_, completedEmitErr := emit.emit(event.ModelCallCompleted, generation.startedID, event.ModelCallPayload{
			CallID: generation.callID, Model: generation.target, Attempt: generation.attempt, Status: "completed",
			StopReason: string(response.StopReason), Usage: response.Usage,
		})
		if completedEmitErr != nil {
			return finish(StatusFailed, "event_sink_failed", &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: completedEmitErr}, nil)
		}

		calls := toolCallsFrom(response.Message)
		if len(calls) == 0 {
			// 无工具调用：必须是 Complete；然后观察本轮并成功结束。
			if response.StopReason != model.StopReasonComplete {
				return finish(StatusFailed, "unexpected_stop", &RunError{Class: ErrorModel, Code: "unexpected_stop_reason", SafeMessage: "The model stopped without completing or requesting a tool."}, nil)
			}
			if err := state.ValidateComplete(); err != nil {
				return finish(StatusFailed, "invalid_final_state", &RunError{Class: ErrorInternal, Code: "invalid_final_state", SafeMessage: "The Agent produced an invalid final state.", Cause: err}, nil)
			}
			final := message.Clone(response.Message)
			observation := a.observeMemory(ctx, runID, state.Messages[turnStart:], emit)
			if observation.err != nil {
				if observation.canceled {
					return finish(StatusCanceled, observation.reason, observation.err, nil)
				}
				return finish(StatusFailed, observation.reason, observation.err, nil)
			}
			return finish(StatusCompleted, "completed", nil, &final)
		}
		// 有工具调用：StopReason 必须是 ToolCall。
		if response.StopReason != model.StopReasonToolCall {
			return finish(StatusFailed, "invalid_tool_stop", &RunError{Class: ErrorModel, Code: "tool_call_stop_mismatch", SafeMessage: "The model returned tool calls with an incompatible stop reason."}, nil)
		}
		if toolCalls+len(calls) > a.config.Limits.MaxToolCalls {
			return finish(StatusFailed, "tool_call_limit", &RunError{Class: ErrorLimit, Code: "max_tool_calls", SafeMessage: "The Agent reached its tool-call limit."}, nil)
		}

		plan, approvalErr := a.approvalPlan(ctx, view, calls)
		if approvalErr != nil {
			if approvalErr.Class == ErrorCanceled {
				return finish(StatusCanceled, "context_canceled", approvalErr, nil)
			}
			return finish(StatusFailed, "approval_policy_failed", approvalErr, nil)
		}
		bindings, batchDigest, freezeErr := a.freezeToolBatch(ctx, transitions, state, view, calls)
		if freezeErr != nil {
			return finish(StatusFailed, "transition_failed", freezeErr, nil)
		}
		if bindingErr := bindApprovalPlanTransitions(plan, bindings); bindingErr != nil {
			return finish(StatusFailed, "invalid_tool_plan", &RunError{Class: ErrorInternal, Code: "invalid_tool_plan", SafeMessage: "The Agent could not bind its Tool execution plan.", Cause: bindingErr}, nil)
		}
		if requiresApproval(plan) {
			// 整批挂起：不执行任何工具，发出 ApprovalRequired，等 Resume。
			suspension := approval.Suspension{ID: a.config.IDs("suspension"), TurnStart: turnStart, Calls: plan}
			nextState := state.Clone()
			nextState.Suspension = &suspension
			if commitErr := a.commitStateTransition(ctx, transitions, &state, nextState, "approval_suspended", ""); commitErr != nil {
				return finish(StatusFailed, "transition_failed", commitErr, nil)
			}
			for _, pending := range plan {
				if pending.Behavior != approval.BehaviorRequire {
					continue
				}
				_, _ = emit.emit(event.ApprovalRequired, "", event.ApprovalPayload{
					ApprovalID: pending.ApprovalID, SuspensionID: suspension.ID, CallID: pending.CallID,
					Tool: pending.Tool, ArgumentsDigest: pending.ArgumentsDigest, ToolDigest: pending.ToolDigest,
					Status: "pending", Reason: pending.Reason,
				})
			}
			return finish(StatusSuspended, "approval_required", nil, nil)
		}
		stop := a.executeToolBatch(ctx, runID, &state, view, calls, plan, bindings, batchDigest, transitions, emit, startedTools, &toolCalls)
		if stop != nil {
			return finish(stop.status, stop.reason, stop.err, nil)
		}
		// 工具结果已写入 State，继续下一轮模型调用。
	}

	return finish(StatusFailed, "iteration_limit", &RunError{Class: ErrorLimit, Code: "max_iterations", SafeMessage: "The Agent reached its iteration limit."}, nil)
}

// toolExecutionStop 表示工具批次应停止 Run（失败/取消），而非把错误回传模型继续聊。
type toolExecutionStop struct {
	status Status
	reason string
	err    *RunError
}

// approvalPlan 对整批 ToolCall 评估 ApprovalPolicy，任一次失败则整批不执行。
func (a *Agent) approvalPlan(ctx context.Context, view toolkit.View, calls []message.ToolCall) ([]approval.PendingCall, *RunError) {
	plan := make([]approval.PendingCall, 0, len(calls))
	for _, call := range calls {
		decision, err := approval.Evaluate(ctx, a.config.ApprovalPolicy, approval.Request{
			CallID: call.ID, Tool: call.Name, Arguments: call.Arguments,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, &RunError{Class: ErrorCanceled, Code: "approval_canceled", SafeMessage: "Tool approval evaluation was canceled.", Cause: ctxErr}
			}
			return nil, &RunError{Class: ErrorApproval, Code: "approval_policy_failed", SafeMessage: "The Tool approval policy failed.", Cause: err}
		}
		toolDigest, exists := view.DefinitionDigest(call.Name)
		if !exists {
			// 未知工具仍进入计划（执行时会走 unsupported_tool），摘要用稳定占位。
			toolDigest = approval.ArgumentsDigest("unsupported", json.RawMessage(`null`))
		}
		pending := approval.PendingCall{
			CallID: call.ID, Tool: call.Name, ArgumentsDigest: approval.ArgumentsDigest(call.Name, call.Arguments),
			ToolDigest: toolDigest, Behavior: decision.Behavior, Reason: decision.Reason,
		}
		if decision.Behavior == approval.BehaviorRequire {
			pending.ApprovalID = a.config.IDs("approval") // 仅 Require 需要人工决议 ID
		}
		plan = append(plan, pending)
	}
	return plan, nil
}

// requiresApproval 判断计划中是否存在需要人工审批的调用。
func requiresApproval(plan []approval.PendingCall) bool {
	for _, call := range plan {
		if call.Behavior == approval.BehaviorRequire {
			return true
		}
	}
	return false
}

// resolveApprovalPlan 用外部 Resolution 填回挂起批次；必须恰好覆盖全部 Require。
func resolveApprovalPlan(suspension approval.Suspension, resolutions []approval.Resolution) ([]approval.PendingCall, error) {
	if err := suspension.Validate(); err != nil {
		return nil, err
	}
	byID := make(map[string]approval.Resolution, len(resolutions))
	for _, resolution := range resolutions {
		resolution.Reason = strings.TrimSpace(resolution.Reason)
		if err := resolution.Validate(); err != nil {
			return nil, err
		}
		if _, exists := byID[resolution.ApprovalID]; exists {
			return nil, fmt.Errorf("duplicate resolution for approval %q", resolution.ApprovalID)
		}
		byID[resolution.ApprovalID] = resolution
	}
	plan := suspension.Clone().Calls
	required := 0
	for index := range plan {
		if plan[index].Behavior != approval.BehaviorRequire {
			continue // 已是 Allow/Deny 的条目不需要 Resolution
		}
		required++
		resolution, exists := byID[plan[index].ApprovalID]
		if !exists {
			return nil, fmt.Errorf("approval %q has no resolution", plan[index].ApprovalID)
		}
		delete(byID, plan[index].ApprovalID)
		if resolution.Status == approval.ResolutionApproved {
			plan[index].Behavior = approval.BehaviorAllow
		} else {
			plan[index].Behavior = approval.BehaviorDeny
		}
		plan[index].Reason = resolution.Reason
	}
	// 数量必须精确匹配：不能多决议、不能少决议。
	if len(resolutions) != required || len(byID) != 0 {
		return nil, fmt.Errorf("approval resolutions do not exactly match the pending set")
	}
	return plan, nil
}

// executeToolBatch 按审批计划顺序执行工具：
// Deny → 写安全错误结果继续；Allow → Call；RuntimeError → 终止 Run；
// 模型可见 Error → 写入结果继续；成功则可能 Offload 大结果。
func (a *Agent) executeToolBatch(
	ctx context.Context,
	runID string,
	state *State,
	view toolkit.View,
	calls []message.ToolCall,
	plan []approval.PendingCall,
	bindings []TransitionToolBinding,
	batchDigest string,
	transitions *transitionEmitter,
	emit *emitter,
	startedTools map[string]string,
	toolCalls *int,
) *toolExecutionStop {
	if err := validateToolPlanBindings(view, calls, plan); err != nil {
		return &toolExecutionStop{status: StatusFailed, reason: "invalid_tool_plan", err: &RunError{
			Class: ErrorInternal, Code: "invalid_tool_plan", SafeMessage: "The Agent produced an invalid Tool execution plan.", Cause: err,
		}}
	}
	if len(bindings) != len(calls) || !validSHA256Digest(batchDigest) {
		return &toolExecutionStop{status: StatusFailed, reason: "invalid_tool_plan", err: &RunError{
			Class: ErrorInternal, Code: "invalid_tool_transition", SafeMessage: "The Agent produced invalid Tool transition bindings.",
		}}
	}
	if err := validateApprovalTransitionBindings(plan, bindings); err != nil {
		return &toolExecutionStop{status: StatusFailed, reason: "invalid_tool_plan", err: &RunError{
			Class: ErrorInternal, Code: "invalid_tool_transition", SafeMessage: "The Agent produced inconsistent Tool effect bindings.", Cause: err,
		}}
	}
	for index, call := range calls {
		planned := plan[index]
		binding := bindings[index]
		// Require 不应进入本函数；挂起应在 run 循环里提前返回。
		if planned.Behavior == approval.BehaviorRequire {
			return &toolExecutionStop{status: StatusFailed, reason: "invalid_tool_plan", err: &RunError{
				Class: ErrorInternal, Code: "invalid_tool_plan", SafeMessage: "The Agent produced an invalid Tool execution plan.",
			}}
		}
		if err := ctx.Err(); err != nil {
			return &toolExecutionStop{status: StatusCanceled, reason: "context_canceled", err: &RunError{Class: ErrorCanceled, Code: "tool_canceled", SafeMessage: "Tool execution was canceled.", Cause: err}}
		}
		(*toolCalls)++
		toolStarted, err := emit.emit(event.ToolCallStarted, "", event.ToolCallPayload{CallID: call.ID, Name: call.Name})
		startedTools[call.ID] = toolStarted.ID // 供 finish/closePendingTools 配对 Completed
		if err != nil {
			return &toolExecutionStop{status: StatusFailed, reason: "event_sink_failed", err: &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: err}}
		}

		if planned.Behavior == approval.BehaviorDeny {
			// 策略拒绝或人工驳回：不调用 Tool，写模型可见错误后继续同批后续调用。
			code := "approval_denied"
			safeMessage := "The Tool call was denied by policy."
			if planned.ApprovalID != "" {
				code = "approval_rejected"
				safeMessage = "The Tool call was rejected by the approver."
			}
			if planned.Reason != "" {
				safeMessage += " " + planned.Reason
			}
			if commitErr := a.appendToolErrorTransition(ctx, transitions, state, call, code, safeMessage, false, "approval_result_committed", ""); commitErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: commitErr}
			}
			_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{
				CallID: call.ID, Name: call.Name, Status: "completed", IsError: true,
				Error: &event.ErrorPayload{Class: "approval", Code: code, Message: safeMessage},
			})
			delete(startedTools, call.ID)
			if emit.sinkErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "event_sink_failed", err: &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}}
			}
			continue
		}

		preparedTransition, transitionErr := commitToolEffectTransition(
			ctx, transitions, TransitionToolEffectPrepared, *state, binding, "", "prepared",
			TransitionPrivateData{ToolArguments: call.Arguments},
		)
		if transitionErr != nil {
			return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: transitionErr}
		}
		startedTransition, transitionErr := commitToolEffectTransition(
			ctx, transitions, TransitionToolEffectStarted, *state, binding, preparedTransition.ID, "started",
			TransitionPrivateData{ToolArguments: call.Arguments},
		)
		if transitionErr != nil {
			return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: transitionErr}
		}

		// 真正执行：Schema 校验与分发在 toolkit.View.Call 内完成。
		toolContext := withToolExecutionTransition(ctx, runID, binding)
		toolResult, toolErr := view.Call(toolContext, call)
		postEffectContext := context.WithoutCancel(ctx)
		commitEffectOutcome := func(kind TransitionType, outcome string, result *tool.Result) (Transition, *RunError) {
			private := TransitionPrivateData{ToolArguments: call.Arguments, ToolResult: result}
			terminal, hookErr := commitToolEffectTransition(
				postEffectContext, transitions, kind, *state, binding, startedTransition.ID, outcome, private,
			)
			if hookErr != nil && kind == TransitionToolEffectCompleted {
				// completed 未能持久化时，只能保守标记为 indeterminate；绝不继续下一外部动作。
				_, _ = commitToolEffectTransition(
					postEffectContext, transitions, TransitionToolEffectIndeterminate, *state, binding, startedTransition.ID, "completion_unpersisted", private,
				)
			}
			return terminal, hookErr
		}
		if ctx.Err() != nil {
			// 调用期间取消：补 canceled 结果并终止 Run。
			terminal, hookErr := commitEffectOutcome(TransitionToolEffectIndeterminate, "canceled", nil)
			if hookErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: hookErr}
			}
			if commitErr := a.appendToolErrorTransition(postEffectContext, transitions, state, call, "canceled", "Tool execution was canceled.", false, "tool_result_committed", terminal.ID); commitErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: commitErr}
			}
			_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{CallID: call.ID, Name: call.Name, Status: "canceled", IsError: true})
			delete(startedTools, call.ID)
			return &toolExecutionStop{status: StatusCanceled, reason: "context_canceled", err: &RunError{Class: ErrorCanceled, Code: "tool_canceled", SafeMessage: "Tool execution was canceled.", Cause: ctx.Err()}}
		}
		if toolErr != nil {
			var runtimeFailure *tool.RuntimeError
			var safe *tool.Error
			if !errors.As(toolErr, &runtimeFailure) && errors.As(toolErr, &safe) {
				// 模型可处理错误：写入 Tool Result，继续同批/下一轮模型。
				terminal, hookErr := commitEffectOutcome(TransitionToolEffectCompleted, "tool_error", nil)
				if hookErr != nil {
					return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: hookErr}
				}
				if commitErr := a.appendToolErrorTransition(postEffectContext, transitions, state, call, safe.Code, safe.SafeMessage, safe.Retryable, "tool_result_committed", terminal.ID); commitErr != nil {
					return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: commitErr}
				}
				_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{
					CallID: call.ID, Name: call.Name, Status: "completed", IsError: true, Retryable: safe.Retryable,
					Error: &event.ErrorPayload{Class: "tool", Code: safe.Code, Message: safe.SafeMessage, Retryable: safe.Retryable},
				})
				delete(startedTools, call.ID)
				if emit.sinkErr != nil {
					return &toolExecutionStop{status: StatusFailed, reason: "event_sink_failed", err: &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: emit.sinkErr}}
				}
				continue
			}
			// RuntimeError / 未知错误：终止整个 Run（不可安全暴露细节给模型）。
			terminal, hookErr := commitEffectOutcome(TransitionToolEffectIndeterminate, "runtime_failure", nil)
			if hookErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: hookErr}
			}
			if commitErr := a.appendToolErrorTransition(postEffectContext, transitions, state, call, "runtime_failure", "Tool execution failed because of a runtime error.", false, "tool_result_committed", terminal.ID); commitErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: commitErr}
			}
			runtimeError := &RunError{Class: ErrorToolRuntime, Code: "tool_runtime_failed", SafeMessage: "A tool runtime failed.", Cause: toolErr}
			_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{CallID: call.ID, Name: call.Name, Status: "failed", IsError: true, Error: eventError(runtimeError)})
			delete(startedTools, call.ID)
			return &toolExecutionStop{status: StatusFailed, reason: "tool_runtime_failed", err: runtimeError}
		}
		terminal, hookErr := commitEffectOutcome(TransitionToolEffectCompleted, "succeeded", &toolResult)
		if hookErr != nil {
			return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: hookErr}
		}

		// 成功：可能把大结果卸载为 Artifact，再写入 State。
		projection := a.projectToolResult(ctx, runID, call, toolResult, toolStarted.ID, emit)
		if projection.err != nil {
			if projection.err.Class == ErrorEventSink {
				return &toolExecutionStop{status: StatusFailed, reason: "event_sink_failed", err: projection.err}
			}
			code := "artifact_unavailable"
			messageText := "The Tool Result could not be stored safely."
			status := "failed"
			if projection.canceled {
				code, messageText, status = "canceled", "Tool Result offload was canceled.", "canceled"
			}
			if commitErr := a.appendToolErrorTransition(postEffectContext, transitions, state, call, code, messageText, false, "tool_result_committed", terminal.ID); commitErr != nil {
				return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: commitErr}
			}
			_, _ = emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{CallID: call.ID, Name: call.Name, Status: status, IsError: true, Error: eventError(projection.err)})
			delete(startedTools, call.ID)
			if projection.canceled {
				return &toolExecutionStop{status: StatusCanceled, reason: projection.reason, err: projection.err}
			}
			return &toolExecutionStop{status: StatusFailed, reason: projection.reason, err: projection.err}
		}
		if commitErr := a.appendToolResultTransition(postEffectContext, transitions, state, call, projection.result, "tool_result_committed", terminal.ID); commitErr != nil {
			return &toolExecutionStop{status: StatusFailed, reason: "transition_failed", err: commitErr}
		}
		_, completedEmitErr := emit.emit(event.ToolCallCompleted, toolStarted.ID, event.ToolCallPayload{CallID: call.ID, Name: call.Name, Status: "completed"})
		delete(startedTools, call.ID)
		if completedEmitErr != nil {
			return &toolExecutionStop{status: StatusFailed, reason: "event_sink_failed", err: &RunError{Class: ErrorEventSink, Code: "event_sink_failed", SafeMessage: "The event consumer failed.", Cause: completedEmitErr}}
		}
	}
	return nil // 整批完成，继续主循环
}

// validateToolPlanBindings 确保审批计划与当前 Toolkit 定义/调用绑定一致（防篡改）。
func validateToolPlanBindings(view toolkit.View, calls []message.ToolCall, plan []approval.PendingCall) error {
	if len(calls) != len(plan) {
		return fmt.Errorf("tool call count %d does not match plan count %d", len(calls), len(plan))
	}
	for index, call := range calls {
		planned := plan[index]
		toolDigest, toolExists := view.DefinitionDigest(call.Name)
		if !toolExists {
			toolDigest = approval.ArgumentsDigest("unsupported", json.RawMessage(`null`))
		}
		// CallID、工具名、参数摘要、定义摘要四者任一不一致即拒绝。
		if planned.CallID != call.ID || planned.Tool != call.Name ||
			planned.ArgumentsDigest != approval.ArgumentsDigest(call.Name, call.Arguments) ||
			planned.ToolDigest != toolDigest {
			return fmt.Errorf("tool call %d does not match its frozen approval binding", index)
		}
	}
	return nil
}

// toolView 按配置的 ToolGroups（及 Skill 投影）获取不可变 Toolkit 快照。
func (a *Agent) toolView() (toolkit.View, error) {
	groups := effectiveToolGroups(a.config.ToolGroups, a.config.Skills)
	if len(groups) == 0 && (a.config.Skills == nil || a.config.Skills.Empty()) {
		return a.config.Toolkit.Snapshot(), nil // 无分组限制：全量快照
	}
	if a.config.Toolkit == nil {
		return a.config.Toolkit.Snapshot(), nil // nil Toolkit 的 Snapshot 返回空 View
	}
	return a.config.Toolkit.SnapshotGroups(groups...)
}

// systemPrompt 组装静态 InstructionStack，并在正文中保留动态上下文边界。
func (a *Agent) systemPrompt() (string, json.RawMessage, error) {
	segments := []contextassembly.Segment{
		contextassembly.StaticSegment(contextassembly.KindSystemPrompt, "agent.config.system_prompt", a.config.SystemPrompt),
	}
	segments = append(segments, a.config.Instructions...)
	segments = append(segments, contextassembly.StaticSegment(contextassembly.KindSkill, "agent.config.skills", a.config.Skills.SystemInstructions()))
	prompt, metadata, err := contextassembly.Render(segments)
	if err != nil || prompt == "" {
		return prompt, nil, err
	}
	encoded, err := contextassembly.MetadataJSON(metadata)
	if err != nil {
		return "", nil, err
	}
	return prompt, encoded, nil
}

// runStartedPayload 构造 run.started：Agent 名、ToolGroups、已激活 Skill、续跑 ID。
func (a *Agent) runStartedPayload(continuationID string) event.RunStartedPayload {
	payload := event.RunStartedPayload{Agent: a.config.Name, ContinuationID: continuationID}
	payload.ToolGroups = effectiveToolGroups(a.config.ToolGroups, a.config.Skills)
	for _, active := range a.config.Skills.Skills() {
		payload.Skills = append(payload.Skills, event.ActivatedSkillPayload{
			Name: active.Name, Version: active.Version, Digest: active.Digest,
		})
	}
	return payload
}

// normalizeInput 填充缺失的 ID/Role/Visibility，默认 User + Public。
func (a *Agent) normalizeInput(input message.Message) message.Message {
	input = message.Clone(input)
	if input.ID == "" {
		input.ID = a.config.IDs("msg")
	}
	if input.Role == "" {
		input.Role = message.RoleUser
	}
	if input.Visibility == "" {
		input.Visibility = message.VisibilityPublic
	}
	return input
}

// normalizeResponse 填充模型响应消息的缺失字段，默认 Assistant + Public。
func (a *Agent) normalizeResponse(response model.Response) model.Response {
	response.Message = message.Clone(response.Message)
	if response.Message.ID == "" {
		response.Message.ID = a.config.IDs("msg")
	}
	if response.Message.Role == "" {
		response.Message.Role = message.RoleAssistant
	}
	if response.Message.Visibility == "" {
		response.Message.Visibility = message.VisibilityPublic
	}
	return response
}

// checkUsage 对照 Limits 检查累计 Token；超限返回 ErrorLimit。
func (a *Agent) checkUsage(usage model.Usage) *RunError {
	limits := a.config.Limits
	switch {
	case usage.InputTokens > limits.MaxInputTokens:
		return &RunError{Class: ErrorLimit, Code: "max_input_tokens", SafeMessage: "The Agent reached its input-token limit."}
	case usage.OutputTokens > limits.MaxOutputTokens:
		return &RunError{Class: ErrorLimit, Code: "max_output_tokens", SafeMessage: "The Agent reached its output-token limit."}
	case usage.ReasoningTokens > limits.MaxReasoningTokens:
		return &RunError{Class: ErrorLimit, Code: "max_reasoning_tokens", SafeMessage: "The Agent reached its reasoning-token limit."}
	default:
		return nil
	}
}

// appendToolResult 追加一条 Role=tool 的消息，并校验 State 仍完整。
func (a *Agent) appendToolResult(state *State, call message.ToolCall, result tool.Result) error {
	nextState := state.Clone()
	nextState.Messages = append(nextState.Messages, message.Message{
		ID: a.config.IDs("msg"), Role: message.RoleTool, Visibility: message.VisibilityInternal,
		Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
			CallID: call.ID, Name: call.Name, Content: message.CloneContent(result.Content), State: append(json.RawMessage(nil), result.State...),
		}}},
	})
	if err := nextState.Validate(); err != nil {
		return err
	}
	*state = nextState
	return nil
}

// appendToolError 把安全错误写成 Tool Result（IsError=true），供模型继续推理。
func (a *Agent) appendToolError(state *State, call message.ToolCall, code, safeMessage string, retryable bool) error {
	metadata, _ := json.Marshal(map[string]string{"code": code})
	if err := a.appendToolResult(state, call, tool.Result{
		Content: []message.Content{{Type: message.ContentText, Text: safeMessage}}, State: metadata,
	}); err != nil {
		return err
	}
	last := &state.Messages[len(state.Messages)-1]
	last.Content[0].ToolResult.IsError = true
	last.Content[0].ToolResult.Retryable = retryable
	return nil
}

// closePendingTools 在非挂起收口时，为尚未有 Result 的 Call 补 interrupted 结果并配对事件。
func (a *Agent) closePendingTools(state *State, emit *emitter, started map[string]string, code, safeMessage string) {
	for _, call := range state.PendingToolCalls() {
		parentID := started[call.ID]
		if parentID == "" {
			// 尚未发出 Started（例如限额打断在执行前）：先补 Started 再 Completed。
			startedEvent, _ := emit.emit(event.ToolCallStarted, "", event.ToolCallPayload{CallID: call.ID, Name: call.Name})
			parentID = startedEvent.ID
		}
		if err := a.appendToolError(state, call, code, safeMessage, false); err != nil {
			continue // 无法写入 State 时跳过，ValidateComplete 随后会失败
		}
		_, _ = emit.emit(event.ToolCallCompleted, parentID, event.ToolCallPayload{
			CallID: call.ID, Name: call.Name, Status: "interrupted", IsError: true,
			Error: &event.ErrorPayload{Class: string(ErrorInternal), Code: code, Message: safeMessage},
		})
		delete(started, call.ID)
	}
}

// toolCallsFrom 从 Assistant 消息中抽出全部 ToolCall（深拷贝 Arguments）。
func toolCallsFrom(value message.Message) []message.ToolCall {
	var calls []message.ToolCall
	for _, content := range value.Content {
		if content.ToolCall != nil {
			call := *content.ToolCall
			call.Arguments = append(json.RawMessage(nil), content.ToolCall.Arguments...)
			calls = append(calls, call)
		}
	}
	return calls
}

// eventError 把 RunError 转成事件载荷（不含 Cause，避免泄露内部细节）。
func eventError(err *RunError) *event.ErrorPayload {
	if err == nil {
		return nil
	}
	return &event.ErrorPayload{Class: string(err.Class), Code: err.Code, Message: err.SafeMessage, Retryable: err.Retryable}
}

// modelRunError 把供应商错误映射为 RunError（保留 Retryable）。
func modelRunError(err error) *RunError {
	var provider *model.ProviderError
	if errors.As(err, &provider) {
		return &RunError{
			Class: ErrorModel, Code: string(provider.Class), SafeMessage: provider.Error(), Retryable: provider.Retryable, Cause: err,
		}
	}
	return &RunError{Class: ErrorModel, Code: "model_failed", SafeMessage: "The model call failed.", Cause: err}
}

// cloneMessagePointer 深拷贝可选消息指针。
func cloneMessagePointer(value *message.Message) *message.Message {
	if value == nil {
		return nil
	}
	cloned := message.Clone(*value)
	return &cloned
}
