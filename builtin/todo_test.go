package builtin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/builtin"
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

func newTodo(t *testing.T, options builtin.TodoOptions) *builtin.TodoTool {
	t.Helper()
	value, err := builtin.NewTodo(options)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
