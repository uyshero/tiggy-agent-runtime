package agenttest

import (
	"context"
	"encoding/json"

	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

type FuncTool struct {
	DefinitionValue model.ToolDefinition
	ExecuteFunc     func(context.Context, json.RawMessage) (tool.Result, error)
}

func (f FuncTool) Definition() model.ToolDefinition {
	return f.DefinitionValue.Clone()
}

func (f FuncTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	return f.ExecuteFunc(ctx, append(json.RawMessage(nil), arguments...))
}
