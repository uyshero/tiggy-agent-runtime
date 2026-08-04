package agenttestbasic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestAgentWithScriptedModelAndTool(t *testing.T) {
	toolCalls := 0
	echo := agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{
			Name:        "echo",
			Description: "Returns the supplied text.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{"text":{"type":"string"}},
				"required":["text"],
				"additionalProperties":false
			}`),
		},
		ExecuteFunc: func(_ context.Context, arguments json.RawMessage) (tool.Result, error) {
			toolCalls++
			var input struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return tool.Result{}, err
			}
			return tool.TextResult(input.Text), nil
		},
	}
	kit, err := toolkit.New(echo)
	if err != nil {
		t.Fatal(err)
	}

	provider := agenttest.NewScriptedModel(
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				if len(request.Tools) != 1 || request.Tools[0].Name != "echo" {
					return fmt.Errorf("tools = %#v, want echo", request.Tools)
				}
				return nil
			},
			Response: model.Response{
				Message: message.Message{Content: []message.Content{{
					Type: message.ContentToolCall,
					ToolCall: &message.ToolCall{
						ID: "call_echo_1", Name: "echo",
						Arguments: json.RawMessage(`{"text":"hello"}`),
					},
				}}},
				StopReason: model.StopReasonToolCall,
			},
		},
		agenttest.ModelStep{
			Assert: func(request model.Request) error {
				if !hasToolResult(request.Messages, "call_echo_1") {
					return fmt.Errorf("second request has no echo Tool Result")
				}
				return nil
			},
			Response: model.Response{
				Message: message.Message{Content: []message.Content{{
					Type: message.ContentText, Text: "finished",
				}}},
				StopReason: model.StopReasonComplete,
			},
		},
	)

	runtime, err := agent.New(agent.Config{
		Name:          "guide-test",
		Model:         provider,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit:       kit,
		IDs:           agenttest.SequentialIDs(),
		Now: agenttest.StepClock(
			time.Unix(1_700_000_000, 0),
			time.Millisecond,
		),
	})
	if err != nil {
		t.Fatal(err)
	}

	var trace agenttest.Trace
	result, err := runtime.RunStream(context.Background(), message.Message{
		Role:       message.RoleUser,
		Visibility: message.VisibilityPublic,
		Content:    []message.Content{{Type: message.ContentText, Text: "repeat hello"}},
	}, trace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if result.Final == nil || result.Final.Content[0].Text != "finished" {
		t.Fatalf("final = %#v", result.Final)
	}
	if len(provider.Calls()) != 2 || toolCalls != 1 {
		t.Fatalf("model calls = %d, tool calls = %d", len(provider.Calls()), toolCalls)
	}
	if err := agenttest.ValidateTrace(trace.Events()); err != nil {
		t.Fatalf("trace: %v\nevent types: %v", err, agenttest.EventTypes(trace.Events()))
	}
}

func hasToolResult(messages []message.Message, callID string) bool {
	for _, value := range messages {
		for _, content := range value.Content {
			if content.ToolResult != nil && content.ToolResult.CallID == callID {
				return true
			}
		}
	}
	return false
}
