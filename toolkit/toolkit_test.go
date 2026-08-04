package toolkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

type fakeTool struct {
	name        string
	schema      json.RawMessage
	concurrency model.ToolConcurrencyClass
	fn          func(context.Context, json.RawMessage) (tool.Result, error)
}

func (f fakeTool) Definition() model.ToolDefinition {
	schema := f.schema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return model.ToolDefinition{Name: f.name, InputSchema: schema, ConcurrencyClass: f.concurrency}
}

func TestInputSchemaIsCompiledAndEnforced(t *testing.T) {
	t.Parallel()

	called := false
	kit, err := toolkit.New(fakeTool{
		name: "counter",
		schema: json.RawMessage(`{
			"type":"object",
			"$defs":{"positive":{"type":"integer","minimum":1}},
			"properties":{"count":{"$ref":"#/$defs/positive"}},
			"required":["count"],
			"additionalProperties":false
		}`),
		fn: func(context.Context, json.RawMessage) (tool.Result, error) {
			called = true
			return tool.TextResult("ok"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_1", Name: "counter", Arguments: json.RawMessage(`{"count":0}`),
	})
	var safe *tool.Error
	if !errors.As(err, &safe) || safe.Code != "invalid_tool_arguments" {
		t.Fatalf("Call() error = %v, want invalid_tool_arguments", err)
	}
	if called {
		t.Fatal("tool executed with schema-invalid arguments")
	}
	if _, err := kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_2", Name: "counter", Arguments: json.RawMessage(`{"count":2}`),
	}); err != nil {
		t.Fatalf("valid call: %v", err)
	}
}

func TestDefinitionPanicIsContained(t *testing.T) {
	t.Parallel()

	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := kit.Register(definitionPanicTool{}); err == nil {
		t.Fatal("Register() did not contain Definition panic")
	}
}

func TestInvalidAndExternalSchemasAreRejected(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		schema json.RawMessage
	}{
		{name: "invalid keyword value", schema: json.RawMessage(`{"type":"not-a-type"}`)},
		{name: "external reference", schema: json.RawMessage(`{"$ref":"https://example.com/schema.json"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := toolkit.New(fakeTool{name: "bad", schema: test.schema, fn: func(context.Context, json.RawMessage) (tool.Result, error) {
				return tool.TextResult("unexpected"), nil
			}})
			if err == nil {
				t.Fatal("New() accepted invalid schema")
			}
		})
	}
}

func TestNamespaceAndGroupSnapshots(t *testing.T) {
	t.Parallel()

	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := kit.RegisterWith(toolkit.Registration{
		Tool: fakeTool{name: "lookup", fn: func(context.Context, json.RawMessage) (tool.Result, error) {
			return tool.TextResult("found"), nil
		}},
		Namespace: "crm", Groups: []string{"read", "customer"},
	}); err != nil {
		t.Fatal(err)
	}
	view, err := kit.SnapshotGroups("read")
	if err != nil {
		t.Fatal(err)
	}
	definitions := view.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "crm.lookup" {
		t.Fatalf("definitions = %#v", definitions)
	}
	if _, err := view.Call(context.Background(), message.ToolCall{
		ID: "call_1", Name: "crm.lookup", Arguments: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := kit.SnapshotGroups("missing"); err == nil {
		t.Fatal("unknown group was accepted")
	}
	groups := kit.Groups()
	if len(groups) != 2 || groups[0] != "customer" || groups[1] != "read" {
		t.Fatalf("groups = %v", groups)
	}
}

func TestSafeBatchRunsConcurrentlyAndPreservesOrder(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	kit, err := toolkit.New(fakeTool{
		name: "safe", concurrency: model.ToolConcurrencySafe,
		fn: func(context.Context, json.RawMessage) (tool.Result, error) {
			started <- struct{}{}
			<-release
			return tool.TextResult("ok"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := []message.ToolCall{
		{ID: "call_1", Name: "safe", Arguments: json.RawMessage(`{}`)},
		{ID: "call_2", Name: "safe", Arguments: json.RawMessage(`{}`)},
	}
	completed := make(chan []toolkit.BatchResult, 1)
	go func() {
		completed <- kit.Snapshot().CallBatch(context.Background(), calls, toolkit.BatchOptions{MaxConcurrency: 2})
	}()
	for count := 0; count < 2; count++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("safe calls did not overlap")
		}
	}
	close(release)
	results := <-completed
	if len(results) != 2 || results[0].CallID != "call_1" || results[1].CallID != "call_2" || results[0].Err != nil || results[1].Err != nil {
		t.Fatalf("batch results = %#v", results)
	}
}

func TestSequentialBatchCallsDoNotOverlap(t *testing.T) {
	t.Parallel()

	var active int32
	var maximum int32
	kit, err := toolkit.New(fakeTool{name: "sequential", fn: func(context.Context, json.RawMessage) (tool.Result, error) {
		current := atomic.AddInt32(&active, 1)
		for {
			observed := atomic.LoadInt32(&maximum)
			if observed >= current || atomic.CompareAndSwapInt32(&maximum, observed, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return tool.TextResult("ok"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	results := kit.Snapshot().CallBatch(context.Background(), []message.ToolCall{
		{ID: "call_1", Name: "sequential", Arguments: json.RawMessage(`{}`)},
		{ID: "call_2", Name: "sequential", Arguments: json.RawMessage(`{}`)},
	}, toolkit.BatchOptions{MaxConcurrency: 2})
	if len(results) != 2 || results[0].Err != nil || results[1].Err != nil {
		t.Fatalf("batch results = %#v", results)
	}
	if got := atomic.LoadInt32(&maximum); got != 1 {
		t.Fatalf("maximum overlap = %d, want 1", got)
	}
}

func (f fakeTool) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	return f.fn(ctx, input)
}

type definitionPanicTool struct{}

func (definitionPanicTool) Definition() model.ToolDefinition {
	panic("definition boom")
}

func (definitionPanicTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.TextResult("unexpected"), nil
}

func TestSnapshotDoesNotChangeAfterRegistration(t *testing.T) {
	t.Parallel()

	first := fakeTool{name: "first", fn: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.TextResult("first"), nil
	}}
	kit, err := toolkit.New(first)
	if err != nil {
		t.Fatal(err)
	}
	view := kit.Snapshot()
	if err := kit.Register(fakeTool{name: "second", fn: first.fn}); err != nil {
		t.Fatal(err)
	}
	if got := len(view.Definitions()); got != 1 {
		t.Fatalf("snapshot definitions = %d, want 1", got)
	}
}

func TestCallContainsPanic(t *testing.T) {
	t.Parallel()

	kit, err := toolkit.New(fakeTool{name: "panic", fn: func(context.Context, json.RawMessage) (tool.Result, error) {
		panic("boom")
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = kit.Snapshot().Call(context.Background(), message.ToolCall{ID: "call_1", Name: "panic", Arguments: json.RawMessage(`{}`)})
	var runtimeError *tool.RuntimeError
	if !errors.As(err, &runtimeError) || runtimeError.Code != "tool_panic" {
		t.Fatalf("Call() error = %v, want tool_panic RuntimeError", err)
	}
}

func TestCallPreservesToolRuntimeError(t *testing.T) {
	t.Parallel()

	expected := &tool.RuntimeError{Code: "permission_policy_failed", Cause: errors.New("policy unavailable")}
	kit, err := toolkit.New(fakeTool{name: "runtime_failure", fn: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{}, expected
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_1", Name: "runtime_failure", Arguments: json.RawMessage(`{}`),
	})
	if err != expected {
		t.Fatalf("Call() error = %v, want original RuntimeError", err)
	}
}

func TestCallReturnsArgumentsErrorToModel(t *testing.T) {
	t.Parallel()

	called := false
	kit, err := toolkit.New(fakeTool{name: "lookup", fn: func(context.Context, json.RawMessage) (tool.Result, error) {
		called = true
		return tool.TextResult("unexpected"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{}`), ArgumentsError: "unterminated object",
	})
	var safe *tool.Error
	if !errors.As(err, &safe) || safe.Code != "invalid_tool_arguments" {
		t.Fatalf("Call() error = %v, want invalid_tool_arguments", err)
	}
	if called {
		t.Fatal("tool executed despite provider argument parse error")
	}
}
