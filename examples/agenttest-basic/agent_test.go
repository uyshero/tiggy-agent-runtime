package agenttestbasic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/memory"
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

func TestInspectInstructionStackContextManifest(t *testing.T) {
	systemPrompt := "Follow runtime rules."
	projectRule := "Use repository conventions."
	taskRule := "Keep this change focused."
	policyRule := "Do not expose secrets."
	knowledge := "This agent runtime favors explicit boundaries."

	provider := agenttest.NewScriptedModel(agenttest.ModelStep{
		Response: model.Response{
			Message: message.Message{Content: []message.Content{{
				Type: message.ContentText, Text: "done",
			}}},
			StopReason: model.StopReasonComplete,
		},
	})
	runtime, err := agent.New(agent.Config{
		Name:         "context-manifest-test",
		SystemPrompt: systemPrompt,
		Instructions: []contextassembly.Segment{
			contextassembly.StaticSegment(contextassembly.KindProject, "repo/AGENTS.md", projectRule),
			contextassembly.StaticSegment(contextassembly.KindTaskRule, "task", taskRule),
			contextassembly.StaticSegment(contextassembly.KindPolicy, "security", policyRule),
			contextassembly.StaticSegment(contextassembly.KindKnowledge, "knowledge", knowledge),
		},
		Model:         provider,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
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
		Content:    []message.Content{{Type: message.ContentText, Text: "summarize the runtime"}},
	}, trace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if err := agenttest.ValidateTrace(trace.Events()); err != nil {
		t.Fatalf("trace: %v", err)
	}

	contextPayload := firstModelContext(t, trace.Events())
	if contextPayload.InstructionStackMessages != 1 {
		t.Fatalf("instruction stack messages = %d", contextPayload.InstructionStackMessages)
	}
	if contextPayload.InstructionStackBytes <= 0 {
		t.Fatalf("instruction stack bytes = %d", contextPayload.InstructionStackBytes)
	}
	if contextPayload.InstructionStackSystemPromptBytes != int64(len(systemPrompt)) ||
		contextPayload.InstructionStackProjectBytes != int64(len(projectRule)) ||
		contextPayload.InstructionStackTaskRuleBytes != int64(len(taskRule)) ||
		contextPayload.InstructionStackPolicyBytes != int64(len(policyRule)) ||
		contextPayload.InstructionStackKnowledgeBytes != int64(len(knowledge)) ||
		contextPayload.InstructionStackSkillBytes != 0 {
		t.Fatalf("instruction stack kind bytes = %#v", contextPayload)
	}
	t.Logf(
		"instruction stack bytes: total=%d system=%d project=%d task_rule=%d policy=%d knowledge=%d skill=%d",
		contextPayload.InstructionStackBytes,
		contextPayload.InstructionStackSystemPromptBytes,
		contextPayload.InstructionStackProjectBytes,
		contextPayload.InstructionStackTaskRuleBytes,
		contextPayload.InstructionStackPolicyBytes,
		contextPayload.InstructionStackKnowledgeBytes,
		contextPayload.InstructionStackSkillBytes,
	)
	if err := agenttest.ValidateInstructionStackBudget(trace.Events(), agenttest.InstructionStackBudget{
		MaxBytes:             1_000,
		MaxSystemPromptBytes: int64(len(systemPrompt)),
		MaxProjectBytes:      int64(len(projectRule)),
		MaxTaskRuleBytes:     int64(len(taskRule)),
		MaxPolicyBytes:       int64(len(policyRule)),
		MaxKnowledgeBytes:    int64(len(knowledge)),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerSelectsToolGroupsBeforeRun(t *testing.T) {
	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range []toolkit.Registration{
		{
			Tool:      exampleTool("lookup_customer"),
			Namespace: "crm",
			Groups:    []string{"customer_read"},
		},
		{
			Tool:      exampleTool("delete_customer"),
			Namespace: "crm",
			Groups:    []string{"customer_write"},
		},
		{
			Tool:      exampleTool("search"),
			Namespace: "kb",
			Groups:    []string{"knowledge_read"},
		},
	} {
		if err := kit.RegisterWith(registration); err != nil {
			t.Fatal(err)
		}
	}

	provider := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			want := []string{"crm.lookup_customer", "kb.search"}
			if got := toolNames(request.Tools); !sameStrings(got, want) {
				return fmt.Errorf("model-visible tools = %v, want %v", got, want)
			}
			return nil
		},
		Response: model.Response{
			Message: message.Message{Content: []message.Content{{
				Type: message.ContentText, Text: "done",
			}}},
			StopReason: model.StopReasonComplete,
		},
	})
	runtime, err := agent.New(agent.Config{
		Name:          "group-consumer-test",
		Model:         provider,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit:       kit,
		ToolGroups:    []string{"customer_read", "knowledge_read"},
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
		Content:    []message.Content{{Type: message.ContentText, Text: "answer from CRM and KB only"}},
	}, trace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if err := agenttest.ValidateTrace(trace.Events()); err != nil {
		t.Fatalf("trace: %v", err)
	}
	contextPayload := firstModelContext(t, trace.Events())
	if contextPayload.ToolDefinitionCount != 2 || contextPayload.ToolDefinitionBytes <= 0 {
		t.Fatalf("tool definition manifest = %#v", contextPayload)
	}
}

func TestValidateContextBudgetForProjectedRequest(t *testing.T) {
	scope := memory.Scope{Namespace: "workspace", Key: "tenant-1:user-1"}
	memoryPort := agenttest.FuncMemory{
		RecallFunc: func(_ context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
			if request.Scope != scope {
				return memory.RecallResult{}, fmt.Errorf("memory scope = %#v, want %#v", request.Scope, scope)
			}
			return memory.RecallResult{Items: []memory.Item{{
				ID:         "memory_1",
				Scope:      scope,
				Text:       "The user prefers concise answers with concrete next steps.",
				Provenance: memory.Provenance{Source: "profile"},
			}}}, nil
		},
	}
	kit, err := toolkit.New(exampleTool("search"))
	if err != nil {
		t.Fatal(err)
	}

	provider := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Tools) != 1 || request.Tools[0].Name != "search" {
				return fmt.Errorf("tools = %#v, want search", request.Tools)
			}
			if !requestContainsText(request, "prefers concise answers") {
				return fmt.Errorf("recalled memory missing from projected request")
			}
			return nil
		},
		Response: model.Response{
			Message: message.Message{Content: []message.Content{{
				Type: message.ContentText, Text: "done",
			}}},
			StopReason: model.StopReasonComplete,
		},
	})
	runtime, err := agent.New(agent.Config{
		Name:          "context-budget-test",
		SystemPrompt:  "Follow runtime rules.",
		Model:         provider,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit:       kit,
		Memory:        memoryPort,
		MemoryScope:   scope,
		MemoryOptions: memory.Options{RecallLimit: 1},
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
		Content:    []message.Content{{Type: message.ContentText, Text: "answer with relevant context"}},
	}, trace.Sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if err := agenttest.ValidateTrace(trace.Events()); err != nil {
		t.Fatalf("trace: %v", err)
	}
	contextPayload := firstModelContext(t, trace.Events())
	if contextPayload.RecalledMemoryMessages != 1 {
		t.Fatalf("recalled memory messages = %d, want 1", contextPayload.RecalledMemoryMessages)
	}
	if contextPayload.ToolDefinitionCount != 1 || contextPayload.ToolDefinitionBytes <= 0 {
		t.Fatalf("tool definition manifest = %#v", contextPayload)
	}
	if err := agenttest.ValidateContextBudget(trace.Events(), agenttest.ContextBudget{
		InstructionStack: agenttest.InstructionStackBudget{
			MaxBytes: 1_000,
		},
		MaxToolDefinitionBytes:    1_000,
		MaxTextBytes:              2_000,
		MaxRequestMessages:        4,
		MaxProjectedMessages:      4,
		MaxToolDefinitions:        1,
		MaxRecalledMemoryMessages: 1,
	}); err != nil {
		t.Fatal(err)
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

func requestContainsText(request model.Request, text string) bool {
	for _, value := range request.Messages {
		for _, content := range value.Content {
			if content.Type == message.ContentText && strings.Contains(content.Text, text) {
				return true
			}
		}
	}
	return false
}

func exampleTool(name string) agenttest.FuncTool {
	return agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{
			Name:        name,
			Description: "Example tool used to demonstrate Tool Group projection.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`),
		},
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.TextResult("ok"), nil
		},
	}
}

func toolNames(definitions []model.ToolDefinition) []string {
	names := make([]string, len(definitions))
	for index, definition := range definitions {
		names[index] = definition.Name
	}
	return names
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func firstModelContext(t *testing.T, events []event.Event) event.ModelContextPayload {
	t.Helper()

	for _, value := range events {
		if value.Type != event.ModelCallStarted {
			continue
		}
		payload, err := agenttest.DecodePayload[event.ModelCallPayload](value)
		if err != nil {
			t.Fatal(err)
		}
		if payload.Context == nil {
			t.Fatal("model.call.started has no context manifest")
		}
		return *payload.Context
	}
	t.Fatal("trace has no model.call.started event")
	return event.ModelContextPayload{}
}
