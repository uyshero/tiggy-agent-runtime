package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/artifact"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestAgentOffloadsLargeToolResultBeforeNextModelCall(t *testing.T) {
	t.Parallel()

	largeText := strings.Repeat("result-data-", 64)
	var stored artifact.PutRequest
	offloader := newAgentOffloader(t, agentStoreFunc(func(_ context.Context, request artifact.PutRequest) (artifact.StoredObject, error) {
		stored = request.Clone()
		return artifact.StoredObject{ObjectRefID: "objects/run-1/result-1"}, nil
	}), 64, 4096)
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "report")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || len(result.Content) != 1 || result.Content[0].Artifact == nil || result.Content[0].Artifact.ObjectRefID != "objects/run-1/result-1" {
					return errors.New("model did not receive the Artifact reference")
				}
				if strings.Contains(string(result.State), largeText) {
					return errors.New("large Tool Result leaked into projected State")
				}
				return nil
			},
			Response: textResponse("done"),
		},
	)
	value := newArtifactAgent(t, script, offloader, largeText)
	result, err := value.Run(context.Background(), userMessage("create report"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	storedResult, err := artifact.DecodeToolResult(stored.Data)
	if err != nil || storedResult.Content[0].Text != largeText {
		t.Fatalf("stored Tool Result = %#v, error = %v", storedResult, err)
	}
	stateResult := lastToolResult(result.State.Messages)
	if stateResult == nil || stateResult.Content[0].Artifact == nil || stateResult.Content[0].Text != "" {
		t.Fatalf("State Tool Result = %#v", stateResult)
	}
	payload := findOffloadPayload(t, result.Events, event.ToolResultOffloadCompleted)
	if payload.Status != "offloaded" || payload.ObjectRefID != "objects/run-1/result-1" || payload.SourceBytes != int64(len(stored.Data)) || payload.Fallback != "none" {
		t.Fatalf("offload payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentKeepsSmallToolResultInline(t *testing.T) {
	t.Parallel()

	storeCalls := 0
	offloader := newAgentOffloader(t, agentStoreFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
		storeCalls++
		return artifact.StoredObject{ObjectRefID: "objects/unexpected"}, nil
	}), 4096, 8192)
	script := agenttest.NewScriptedModel(
		agenttest.ModelStep{Response: toolCallResponse("call_1", "report")},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				result := lastToolResult(request.Messages)
				if result == nil || result.Content[0].Text != "small" || result.Content[0].Artifact != nil {
					return errors.New("small result was not kept inline")
				}
				return nil
			},
			Response: textResponse("done"),
		},
	)
	result, err := newArtifactAgent(t, script, offloader, "small").Run(context.Background(), userMessage("create report"))
	if err != nil {
		t.Fatal(err)
	}
	if storeCalls != 0 {
		t.Fatalf("store calls = %d", storeCalls)
	}
	payload := findOffloadPayload(t, result.Events, event.ToolResultOffloadCompleted)
	if payload.Status != "inline" || payload.ObjectRefID != "" {
		t.Fatalf("offload payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentFailsClosedOnArtifactOffloaderFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		offloader artifact.Offloader
		code      string
	}{
		{name: "error", offloader: agenttest.FuncOffloader(func(context.Context, artifact.Request) (artifact.Decision, error) {
			return artifact.Decision{}, errors.New("offline")
		}), code: "artifact_offload_failed"},
		{name: "panic", offloader: agenttest.FuncOffloader(func(context.Context, artifact.Request) (artifact.Decision, error) {
			panic("boom")
		}), code: "artifact_offload_failed"},
		{name: "invalid decision", offloader: agenttest.FuncOffloader(func(context.Context, artifact.Request) (artifact.Decision, error) {
			return artifact.Decision{Offloaded: true}, nil
		}), code: "invalid_artifact_offload"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "report")})
			result, err := newArtifactAgent(t, script, test.offloader, strings.Repeat("x", 128)).Run(context.Background(), userMessage("create report"))
			assertRunError(t, err, agent.ErrorArtifact, test.code)
			projected := lastToolResult(result.State.Messages)
			if projected == nil || !projected.IsError || projected.Content[0].Text != "The Tool Result could not be stored safely." {
				t.Fatalf("Artifact failure Tool Result = %#v", projected)
			}
			if countEventType(result.Events, event.ToolResultOffloadFailed) != 1 || len(script.Calls()) != 1 {
				t.Fatalf("events = %#v", agenttest.EventTypes(result.Events))
			}
			if err := agenttest.ValidateTrace(result.Events); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentArtifactOffloadCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	offloader := agenttest.FuncOffloader(func(callCtx context.Context, _ artifact.Request) (artifact.Decision, error) {
		cancel()
		<-callCtx.Done()
		return artifact.Decision{}, callCtx.Err()
	})
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "report")})
	result, err := newArtifactAgent(t, script, offloader, strings.Repeat("x", 128)).Run(ctx, userMessage("create report"))
	assertRunError(t, err, agent.ErrorCanceled, "artifact_offload_canceled")
	if result.Status != agent.StatusCanceled {
		t.Fatalf("status = %q", result.Status)
	}
	payload := findOffloadPayload(t, result.Events, event.ToolResultOffloadFailed)
	if payload.Status != "canceled" || payload.Fallback != "fail_closed" {
		t.Fatalf("offload payload = %#v", payload)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentClassifiesArtifactPayloadLimit(t *testing.T) {
	t.Parallel()

	offloader := newAgentOffloader(t, agentStoreFunc(func(context.Context, artifact.PutRequest) (artifact.StoredObject, error) {
		t.Fatal("store called for payload over maximum")
		return artifact.StoredObject{}, nil
	}), 1, 64)
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "report")})
	result, err := newArtifactAgent(t, script, offloader, strings.Repeat("x", 256)).Run(context.Background(), userMessage("create report"))
	assertRunError(t, err, agent.ErrorLimit, "artifact_payload_limit")
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func TestAgentArtifactEventSinkFailureKeepsBalancedTrace(t *testing.T) {
	t.Parallel()

	offloader := agenttest.FuncOffloader(func(context.Context, artifact.Request) (artifact.Decision, error) {
		return artifact.Decision{}, nil
	})
	script := agenttest.NewScriptedModel(agenttest.ModelStep{Response: toolCallResponse("call_1", "report")})
	value := newArtifactAgent(t, script, offloader, "small")
	result, err := value.RunStream(context.Background(), userMessage("create report"), func(value event.Event) error {
		if value.Type == event.ToolResultOffloadStarted {
			return errors.New("sink offline")
		}
		return nil
	})
	assertRunError(t, err, agent.ErrorEventSink, "event_sink_failed")
	if countEventType(result.Events, event.ToolResultOffloadStarted) != 1 || countEventType(result.Events, event.ToolResultOffloadFailed) != 1 {
		t.Fatalf("offload events = %#v", agenttest.EventTypes(result.Events))
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

type agentStoreFunc func(context.Context, artifact.PutRequest) (artifact.StoredObject, error)

func (f agentStoreFunc) Put(ctx context.Context, request artifact.PutRequest) (artifact.StoredObject, error) {
	return f(ctx, request.Clone())
}

func newAgentOffloader(t *testing.T, store artifact.Store, inline, maximum int64) *artifact.BoundedOffloader {
	t.Helper()
	offloader, err := artifact.NewBoundedOffloader(artifact.BoundedOffloaderOptions{
		Store: store, MaxInlineBytes: inline, MaxArtifactBytes: maximum,
	})
	if err != nil {
		t.Fatal(err)
	}
	return offloader
}

func newArtifactAgent(t *testing.T, provider model.Model, offloader artifact.Offloader, resultText string) *agent.Agent {
	t.Helper()
	kit, err := toolkit.New(agenttest.FuncTool{
		DefinitionValue: toolDefinition("report"),
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.TextResult(resultText), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := agent.New(agent.Config{
		Name: "artifact-test", Model: provider, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit: kit, ArtifactOffloader: offloader, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func findOffloadPayload(t *testing.T, events []event.Event, eventType event.Type) event.ToolResultOffloadPayload {
	t.Helper()
	for _, value := range events {
		if value.Type == eventType {
			payload, err := agenttest.DecodePayload[event.ToolResultOffloadPayload](value)
			if err != nil {
				t.Fatal(err)
			}
			return payload
		}
	}
	t.Fatalf("%s event not found", eventType)
	return event.ToolResultOffloadPayload{}
}
