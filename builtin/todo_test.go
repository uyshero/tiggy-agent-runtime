package builtin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/builtin"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestTodoRoundTripAndStatefulUpdates(t *testing.T) {
	t.Parallel()

	todo := newTodo(t, builtin.TodoOptions{})
	add, err := todo.Execute(context.Background(), json.RawMessage(`{"action":"add","title":"write docs"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(add.Content) != 1 || !strings.Contains(add.Content[0].Text, `Added todo "todo_1".`) {
		t.Fatalf("add result = %#v", add)
	}
	var state builtin.TodoState
	if err := json.Unmarshal(add.State, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Items) != 1 || state.Items[0].ID != "todo_1" || state.Items[0].Title != "write docs" || state.Items[0].Done {
		t.Fatalf("add state = %#v", state)
	}

	complete, err := todo.Execute(context.Background(), json.RawMessage(`{
		"action":"complete",
		"state":`+string(add.State)+`,
		"id":"todo_1"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(complete.Content) != 1 || !strings.Contains(complete.Content[0].Text, `Marked todo "todo_1" done.`) {
		t.Fatalf("complete result = %#v", complete)
	}
	if err := json.Unmarshal(complete.State, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Items) != 1 || !state.Items[0].Done {
		t.Fatalf("complete state = %#v", state)
	}

	list, err := todo.Execute(context.Background(), json.RawMessage(`{
		"action":"list",
		"state":`+string(complete.State)+`
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Content) != 1 || !strings.Contains(list.Content[0].Text, `Todo list: 1 item(s), 1 done.`) || !strings.Contains(list.Content[0].Text, `- [x] write docs (todo_1)`) {
		t.Fatalf("list result = %#v", list)
	}
}

func TestTodoRejectsInvalidStateAndItemLimit(t *testing.T) {
	t.Parallel()

	todo := newTodo(t, builtin.TodoOptions{MaxItems: 1})
	first, err := todo.Execute(context.Background(), json.RawMessage(`{
		"action":"add","title":"one"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = todo.Execute(context.Background(), json.RawMessage(`{
		"action":"add",
		"state":`+string(first.State)+`,
		"title":"two"
	}`))
	assertToolCode(t, err, "todo_limit_exceeded")

	_, err = todo.Execute(context.Background(), json.RawMessage(`{
		"action":"list",
		"state":{"version":1,"next_id":1,"items":[
			{"id":"dup","title":"one","done":false},
			{"id":"dup","title":"two","done":false}
		]}
	}`))
	assertToolCode(t, err, "invalid_tool_arguments")
}

func TestTodoSnapshotAliasIsDeclaredInSchema(t *testing.T) {
	t.Parallel()

	todo := newTodo(t, builtin.TodoOptions{})
	kit, err := toolkit.New(todo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := kit.Snapshot().Call(context.Background(), message.ToolCall{
		ID: "call_todo", Name: builtin.TodoToolName, Arguments: json.RawMessage(`{"action":"snapshot"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "Todo list: 0 item(s), 0 done.") {
		t.Fatalf("snapshot result = %#v", result)
	}
}

func TestExtractLatestTodoStateFromMessages(t *testing.T) {
	t.Parallel()

	todo := newTodo(t, builtin.TodoOptions{})
	add, err := todo.Execute(context.Background(), json.RawMessage(`{"action":"add","title":"draft plan"}`))
	if err != nil {
		t.Fatal(err)
	}
	complete, err := todo.Execute(context.Background(), json.RawMessage(`{
		"action":"complete",
		"state":`+string(add.State)+`,
		"id":"todo_1"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := []message.Message{
		todoResultMessage("msg_old", "call_old", add.State, false),
		{
			ID: "msg_other", Role: message.RoleTool, Visibility: message.VisibilityInternal,
			Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
				CallID: "call_other", Name: builtin.ReadToolName, Content: []message.Content{{Type: message.ContentText, Text: "ignored"}},
			}}},
		},
		todoResultMessage("msg_new", "call_new", complete.State, false),
	}

	state, ok, err := builtin.ExtractLatestTodoState(messages)
	if err != nil || !ok {
		t.Fatalf("ExtractLatestTodoState() = ok %v, err %v", ok, err)
	}
	if len(state.Items) != 1 || state.Items[0].ID != "todo_1" || !state.Items[0].Done {
		t.Fatalf("extracted state = %#v", state)
	}
	state.Items[0].Title = "mutated"
	again, ok, err := builtin.ExtractLatestTodoState(messages)
	if err != nil || !ok || again.Items[0].Title != "draft plan" {
		t.Fatalf("extracted state shared mutable data: %#v, ok %v, err %v", again, ok, err)
	}
}

func TestExtractLatestTodoStateReportsCorruptState(t *testing.T) {
	t.Parallel()

	_, ok, err := builtin.ExtractLatestTodoState([]message.Message{
		todoResultMessage("msg_bad", "call_bad", json.RawMessage(`{"version":1,"next_id":1,"items":[{"id":"dup","title":"one","done":false},{"id":"dup","title":"two","done":false}]}`), false),
	})
	if !ok || err == nil {
		t.Fatalf("ExtractLatestTodoState() = ok %v, err %v; want corrupt state error", ok, err)
	}

	_, ok, err = builtin.ExtractLatestTodoState([]message.Message{
		todoResultMessage("msg_error", "call_error", json.RawMessage(`{}`), true),
	})
	if ok || err != nil {
		t.Fatalf("error todo result should be ignored: ok %v, err %v", ok, err)
	}
}

func newTodo(t *testing.T, options builtin.TodoOptions) *builtin.TodoTool {
	t.Helper()
	value, err := builtin.NewTodo(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func todoResultMessage(id, callID string, state json.RawMessage, isError bool) message.Message {
	return message.Message{
		ID: id, Role: message.RoleTool, Visibility: message.VisibilityInternal,
		Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
			CallID: callID, Name: builtin.TodoToolName, Content: []message.Content{{Type: message.ContentText, Text: "todo state"}},
			State: state, IsError: isError,
		}}},
	}
}
