// Package agenttest provides deterministic test doubles and trace assertions
// for Tiggy Agent Runtime integrations.
package agenttest

import (
	"context"
	"fmt"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/artifact"
	"github.com/uyshero/tiggy-agent-runtime/contextmanager"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type ModelStep struct {
	Response model.Response
	Err      error
	Deltas   []model.Delta
	Assert   func(model.Request) error
}

type ScriptedModel struct {
	mu    sync.Mutex
	steps []ModelStep
	calls []model.Request
	next  int
}

func NewScriptedModel(steps ...ModelStep) *ScriptedModel {
	return &ScriptedModel{steps: append([]ModelStep(nil), steps...)}
}

func (m *ScriptedModel) Generate(ctx context.Context, request model.Request, sink model.DeltaSink) (model.Response, error) {
	m.mu.Lock()
	if m.next >= len(m.steps) {
		m.mu.Unlock()
		return model.Response{}, fmt.Errorf("agenttest: unexpected model call %d", m.next+1)
	}
	step := m.steps[m.next]
	m.next++
	m.calls = append(m.calls, cloneRequest(request))
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	if step.Assert != nil {
		if err := step.Assert(cloneRequest(request)); err != nil {
			return model.Response{}, fmt.Errorf("agenttest: request assertion: %w", err)
		}
	}
	for _, delta := range step.Deltas {
		if sink != nil {
			if err := sink(delta); err != nil {
				return model.Response{}, err
			}
		}
	}
	return cloneResponse(step.Response), step.Err
}

func (m *ScriptedModel) Calls() []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := make([]model.Request, len(m.calls))
	for index, call := range m.calls {
		values[index] = cloneRequest(call)
	}
	return values
}

type FuncModel func(context.Context, model.Request, model.DeltaSink) (model.Response, error)

func (f FuncModel) Generate(ctx context.Context, request model.Request, sink model.DeltaSink) (model.Response, error) {
	return f(ctx, request, sink)
}

type FuncTokenCounter func(context.Context, model.TokenRequest) (int64, error)

func (f FuncTokenCounter) CountTokens(ctx context.Context, request model.TokenRequest) (int64, error) {
	return f(ctx, cloneTokenRequest(request))
}

type FuncPlanner func(context.Context, contextmanager.Request) (contextmanager.Plan, error)

func (f FuncPlanner) Plan(ctx context.Context, request contextmanager.Request) (contextmanager.Plan, error) {
	return f(ctx, cloneContextRequest(request))
}

type FuncCompressor func(context.Context, contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error)

func (f FuncCompressor) Compress(ctx context.Context, request contextmanager.CompressionRequest) (contextmanager.CompressionDecision, error) {
	return f(ctx, request.Clone())
}

type FuncOffloader func(context.Context, artifact.Request) (artifact.Decision, error)

func (f FuncOffloader) Offload(ctx context.Context, request artifact.Request) (artifact.Decision, error) {
	return f(ctx, request.Clone())
}

func cloneRequest(value model.Request) model.Request {
	cloned := value
	cloned.Messages = message.CloneAll(value.Messages)
	cloned.Tools = make([]model.ToolDefinition, len(value.Tools))
	for index, definition := range value.Tools {
		cloned.Tools[index] = definition.Clone()
	}
	cloned.Parameters = append([]byte(nil), value.Parameters...)
	return cloned
}

func cloneResponse(value model.Response) model.Response {
	cloned := value
	cloned.Message = message.Clone(value.Message)
	return cloned
}

func cloneTokenRequest(value model.TokenRequest) model.TokenRequest {
	cloned := value
	cloned.Messages = message.CloneAll(value.Messages)
	cloned.Tools = make([]model.ToolDefinition, len(value.Tools))
	for index, definition := range value.Tools {
		cloned.Tools[index] = definition.Clone()
	}
	return cloned
}
