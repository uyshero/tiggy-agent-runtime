package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/artifact"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

// toolResultProjection 是工具结果投影（可能卸载为 Artifact）后的编排结果。
type toolResultProjection struct {
	result   tool.Result
	err      *RunError
	reason   string
	canceled bool
}

// projectToolResult 在配置了 Offloader 时，把超限 Tool Result 卸载到 Artifact Store。
func (a *Agent) projectToolResult(
	ctx context.Context,
	runID string,
	call message.ToolCall,
	result tool.Result,
	toolStartedID string,
	emit *emitter,
) toolResultProjection {
	if a.config.ArtifactOffloader == nil {
		return toolResultProjection{result: cloneToolResult(result)}
	}
	request, requestErr := artifact.NewRequest(runID, call, result)
	if requestErr != nil {
		return toolResultProjection{
			err:    &RunError{Class: ErrorInternal, Code: "invalid_artifact_request", SafeMessage: "The Agent could not prepare a Tool Result artifact.", Cause: requestErr},
			reason: "artifact_offload_failed",
		}
	}
	started, emitErr := emit.emit(event.ToolResultOffloadStarted, toolStartedID, event.ToolResultOffloadPayload{
		CallID: call.ID, Name: call.Name, SourceBytes: int64(len(request.Payload)),
		MediaType: request.MediaType, Digest: request.Digest,
	})
	if emitErr != nil {
		runtimeError := eventSinkRunError(emitErr)
		_, _ = emit.emit(event.ToolResultOffloadFailed, started.ID, offloadFailurePayload(request, "failed", runtimeError))
		return toolResultProjection{err: runtimeError, reason: "event_sink_failed"}
	}
	decision, offloadErr := callOffloader(ctx, a.config.ArtifactOffloader, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		runtimeError := &RunError{Class: ErrorCanceled, Code: "artifact_offload_canceled", SafeMessage: "Tool Result offload was canceled.", Cause: ctxErr}
		_, _ = emit.emit(event.ToolResultOffloadFailed, started.ID, offloadFailurePayload(request, "canceled", runtimeError))
		return toolResultProjection{err: runtimeError, reason: "context_canceled", canceled: true}
	}
	if offloadErr != nil {
		runtimeError := artifactRunError(offloadErr)
		_, completedErr := emit.emit(event.ToolResultOffloadFailed, started.ID, offloadFailurePayload(request, "failed", runtimeError))
		if completedErr != nil {
			return toolResultProjection{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return toolResultProjection{err: runtimeError, reason: "artifact_offload_failed"}
	}
	projected, applyErr := artifact.ApplyDecision(result, request, decision)
	if applyErr != nil {
		runtimeError := &RunError{Class: ErrorArtifact, Code: "invalid_artifact_offload", SafeMessage: "The Artifact Offloader returned an invalid decision.", Cause: applyErr}
		_, completedErr := emit.emit(event.ToolResultOffloadFailed, started.ID, offloadFailurePayload(request, "failed", runtimeError))
		if completedErr != nil {
			return toolResultProjection{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
		}
		return toolResultProjection{err: runtimeError, reason: "artifact_offload_failed"}
	}
	payload := event.ToolResultOffloadPayload{
		CallID: call.ID, Name: call.Name, Status: "inline", SourceBytes: int64(len(request.Payload)),
		MediaType: request.MediaType, Digest: request.Digest, Fallback: "none",
	}
	if decision.Offloaded {
		payload.Status = "offloaded"
		payload.ObjectRefID = decision.Reference.ObjectRefID
	}
	_, completedErr := emit.emit(event.ToolResultOffloadCompleted, started.ID, payload)
	if completedErr != nil {
		return toolResultProjection{err: eventSinkRunError(completedErr), reason: "event_sink_failed"}
	}
	return toolResultProjection{result: projected}
}

func callOffloader(ctx context.Context, offloader artifact.Offloader, request artifact.Request) (decision artifact.Decision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("artifact offloader panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return offloader.Offload(ctx, request.Clone())
}

func artifactRunError(cause error) *RunError {
	var artifactError *artifact.Error
	if errors.As(cause, &artifactError) && artifactError.Code == artifact.ErrorPayloadLimit {
		return &RunError{Class: ErrorLimit, Code: "artifact_payload_limit", SafeMessage: "The Tool Result exceeds the configured Artifact limit.", Cause: cause}
	}
	return &RunError{Class: ErrorArtifact, Code: "artifact_offload_failed", SafeMessage: "The Agent could not store a Tool Result artifact.", Cause: cause}
}

func offloadFailurePayload(request artifact.Request, status string, runtimeError *RunError) event.ToolResultOffloadPayload {
	return event.ToolResultOffloadPayload{
		CallID: request.CallID, Name: request.ToolName, Status: status,
		SourceBytes: int64(len(request.Payload)), MediaType: request.MediaType, Digest: request.Digest,
		Fallback: "fail_closed", Error: eventError(runtimeError),
	}
}

func cloneToolResult(result tool.Result) tool.Result {
	return tool.Result{Content: message.CloneContent(result.Content), State: append([]byte(nil), result.State...)}
}
