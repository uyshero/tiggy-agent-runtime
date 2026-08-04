package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func main() {
	kit, err := toolkit.New(clockTool{})
	if err != nil {
		log.Fatal(err)
	}
	runtime, err := agent.New(agent.Config{
		Name:          "clock-assistant",
		SystemPrompt:  "Use tools when needed.",
		Model:         demoModel{},
		ModelIdentity: model.Identity{Provider: "example", Model: "deterministic-demo"},
		Toolkit:       kit,
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := runtime.RunStream(context.Background(), message.Message{
		Role:       message.RoleUser,
		Visibility: message.VisibilityPublic,
		Content:    []message.Content{{Type: message.ContentText, Text: "What time is it?"}},
	}, func(value event.Event) error {
		fmt.Printf("%02d  %s\n", value.Sequence, value.Type)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Final.Content[0].Text)
}

type clockTool struct{}

func (clockTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "current_time", Description: "Return the current UTC time.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
	}
}

func (clockTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.TextResult(time.Now().UTC().Format(time.RFC3339)), nil
}

// demoModel keeps the example runnable without credentials. Replace it with a
// provider adapter implementing model.Model in an actual application.
type demoModel struct{}

func (demoModel) Generate(_ context.Context, request model.Request, _ model.DeltaSink) (model.Response, error) {
	last := request.Messages[len(request.Messages)-1]
	if last.Role != message.RoleTool {
		return model.Response{
			Message: message.Message{Content: []message.Content{{
				Type:     message.ContentToolCall,
				ToolCall: &message.ToolCall{ID: "call_current_time", Name: "current_time", Arguments: json.RawMessage(`{}`)},
			}}},
			StopReason: model.StopReasonToolCall,
		}, nil
	}
	value := last.Content[0].ToolResult.Content[0].Text
	return model.Response{
		Message:    message.Message{Content: []message.Content{{Type: message.ContentText, Text: "Current UTC time: " + value}}},
		StopReason: model.StopReasonComplete,
	}, nil
}
