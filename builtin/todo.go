package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/internal/strictjson"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	// TodoToolName 是 TodoTool 对模型可见的稳定名称。
	TodoToolName = "builtin.todo"
	// DefaultMaxTodoItems 默认限制列表项数量。
	DefaultMaxTodoItems = 64
	// DefaultMaxTodoTitleBytes 默认限制单条标题大小。
	DefaultMaxTodoTitleBytes = 256
)

// TodoOptions 配置 TodoTool。
type TodoOptions struct {
	MaxItems      int
	MaxTitleBytes int
}

// TodoItem 是一条结构化待办。
type TodoItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// TodoState 是 TodoTool 的可序列化运行态。
type TodoState struct {
	Version int        `json:"version"`
	NextID  int        `json:"next_id"`
	Items   []TodoItem `json:"items"`
}

// TodoTool 维护一个 run-local 的结构化待办列表。
type TodoTool struct {
	maxItems      int
	maxTitleBytes int
}

var _ tool.Tool = (*TodoTool)(nil)

// NewTodo 构造一个有界待办工具。
func NewTodo(options TodoOptions) (*TodoTool, error) {
	maxItems := options.MaxItems
	if maxItems == 0 {
		maxItems = DefaultMaxTodoItems
	}
	if maxItems < 0 {
		return nil, fmt.Errorf("todo maximum items must be positive")
	}
	maxTitleBytes := options.MaxTitleBytes
	if maxTitleBytes == 0 {
		maxTitleBytes = DefaultMaxTodoTitleBytes
	}
	if maxTitleBytes < 0 {
		return nil, fmt.Errorf("todo maximum title bytes must be positive")
	}
	return &TodoTool{maxItems: maxItems, maxTitleBytes: maxTitleBytes}, nil
}

func (t *TodoTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:             TodoToolName,
		Description:      "Maintain a bounded run-local todo list and return the current snapshot.",
		ConcurrencyClass: model.ToolConcurrencySequential,
		InputSchema: json.RawMessage(fmt.Sprintf(`{
			"type":"object",
			"properties":{
				"action":{"type":"string","enum":["list","snapshot","add","update","complete","remove","clear"]},
				"state":{
					"type":"object",
					"properties":{
						"version":{"type":"integer","minimum":1},
						"next_id":{"type":"integer","minimum":1},
						"items":{
							"type":"array",
							"maxItems":%d,
							"items":{
								"type":"object",
								"properties":{
									"id":{"type":"string","minLength":1},
									"title":{"type":"string","minLength":1},
									"done":{"type":"boolean"}
								},
								"required":["id","title","done"],
								"additionalProperties":false
							}
						}
					},
					"additionalProperties":false
				},
				"id":{"type":"string","minLength":1},
				"title":{"type":"string","minLength":1,"maxLength":%d},
				"done":{"type":"boolean"}
			},
			"required":["action"],
			"additionalProperties":false
		}`, t.maxItems, t.maxTitleBytes)),
	}
}

func (t *TodoTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	var input struct {
		Action string     `json:"action"`
		State  *TodoState `json:"state"`
		ID     string     `json:"id"`
		Title  string     `json:"title"`
		Done   *bool      `json:"done"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return tool.Result{}, err
	}
	state := cloneTodoState(input.State)
	if err := state.Validate(t.maxItems, t.maxTitleBytes); err != nil {
		return tool.Result{}, invalidArguments(err)
	}
	summary := ""
	action := strings.ToLower(strings.TrimSpace(input.Action))

	switch action {
	case "list", "snapshot":
		summary = fmt.Sprintf("Todo list: %d item(s), %d done.", len(state.Items), countTodoDone(state.Items))
	case "add":
		if strings.TrimSpace(input.Title) == "" {
			return tool.Result{}, invalidArguments(fmt.Errorf("title is required"))
		}
		if len(input.Title) > t.maxTitleBytes {
			return tool.Result{}, tool.NewError("todo_limit_exceeded", "The todo title exceeds the configured size limit.", false, nil)
		}
		if len(state.Items) >= t.maxItems {
			return tool.Result{}, tool.NewError("todo_limit_exceeded", "The todo list exceeds the configured item limit.", false, nil)
		}
		id := state.nextAvailableID()
		state.Items = append(state.Items, TodoItem{ID: id, Title: input.Title, Done: false})
		summary = fmt.Sprintf("Added todo %q.", id)
	case "update":
		if strings.TrimSpace(input.ID) == "" {
			return tool.Result{}, invalidArguments(fmt.Errorf("id is required"))
		}
		if strings.TrimSpace(input.Title) == "" && input.Done == nil {
			return tool.Result{}, invalidArguments(fmt.Errorf("update requires title or done"))
		}
		index := state.indexOf(input.ID)
		if index < 0 {
			return tool.Result{}, tool.NewError("todo_not_found", "The requested todo item was not found.", false, nil)
		}
		if input.Title != "" {
			if len(input.Title) > t.maxTitleBytes {
				return tool.Result{}, tool.NewError("todo_limit_exceeded", "The todo title exceeds the configured size limit.", false, nil)
			}
			state.Items[index].Title = input.Title
		}
		if input.Done != nil {
			state.Items[index].Done = *input.Done
		}
		summary = fmt.Sprintf("Updated todo %q.", input.ID)
	case "complete":
		if strings.TrimSpace(input.ID) == "" {
			return tool.Result{}, invalidArguments(fmt.Errorf("id is required"))
		}
		index := state.indexOf(input.ID)
		if index < 0 {
			return tool.Result{}, tool.NewError("todo_not_found", "The requested todo item was not found.", false, nil)
		}
		state.Items[index].Done = true
		summary = fmt.Sprintf("Marked todo %q done.", input.ID)
	case "remove":
		if strings.TrimSpace(input.ID) == "" {
			return tool.Result{}, invalidArguments(fmt.Errorf("id is required"))
		}
		index := state.indexOf(input.ID)
		if index < 0 {
			return tool.Result{}, tool.NewError("todo_not_found", "The requested todo item was not found.", false, nil)
		}
		state.Items = append(append([]TodoItem(nil), state.Items[:index]...), state.Items[index+1:]...)
		summary = fmt.Sprintf("Removed todo %q.", input.ID)
	case "clear":
		cleared := len(state.Items)
		state.Items = []TodoItem{}
		state.NextID = 1
		summary = fmt.Sprintf("Cleared %d todo item(s).", cleared)
	default:
		return tool.Result{}, invalidArguments(fmt.Errorf("unsupported todo action %q", input.Action))
	}

	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := state.Validate(t.maxItems, t.maxTitleBytes); err != nil {
		return tool.Result{}, invalidArguments(err)
	}
	if state.Items == nil {
		state.Items = []TodoItem{}
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return tool.Result{}, &tool.RuntimeError{Code: "todo_state_marshal_failed", Cause: err}
	}
	return tool.Result{
		Content: []message.Content{{Type: message.ContentText, Text: renderTodoText(summary, state.Items)}},
		State:   stateJSON,
	}, nil
}

// ExtractLatestTodoState 从消息历史中提取最近一次成功 builtin.todo Tool Result
// 携带的结构化状态。它不执行工具、不修改消息，也不把 Todo 变成持久化任务系统；
// 调用方可用它在测试、UI 或应用层恢复 run-local checklist 的当前快照。
func ExtractLatestTodoState(messages []message.Message) (TodoState, bool, error) {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		contents := messages[messageIndex].Content
		for contentIndex := len(contents) - 1; contentIndex >= 0; contentIndex-- {
			result := contents[contentIndex].ToolResult
			if result == nil || result.Name != TodoToolName || result.IsError {
				continue
			}
			if len(result.State) == 0 {
				return TodoState{}, true, fmt.Errorf("todo tool result state is missing")
			}
			var state TodoState
			if err := strictjson.DecodeBytes(result.State, &state); err != nil {
				return TodoState{}, true, fmt.Errorf("todo tool result state: %w", err)
			}
			cloned := cloneTodoState(&state)
			if err := cloned.Validate(0, 0); err != nil {
				return TodoState{}, true, fmt.Errorf("todo tool result state: %w", err)
			}
			return *cloned, true, nil
		}
	}
	return TodoState{}, false, nil
}

func cloneTodoState(value *TodoState) *TodoState {
	if value == nil {
		return &TodoState{Version: 1, NextID: 1}
	}
	cloned := *value
	cloned.Items = append([]TodoItem(nil), value.Items...)
	if cloned.Items == nil {
		cloned.Items = []TodoItem{}
	}
	if cloned.Version == 0 {
		cloned.Version = 1
	}
	if cloned.NextID < 1 {
		cloned.NextID = 1
	}
	return &cloned
}

func (s *TodoState) Validate(maxItems, maxTitleBytes int) error {
	if s == nil {
		return fmt.Errorf("todo state is required")
	}
	if s.Version != 1 {
		return fmt.Errorf("unsupported todo state version %d", s.Version)
	}
	if s.NextID < 1 {
		return fmt.Errorf("todo state next_id must be positive")
	}
	if maxItems > 0 && len(s.Items) > maxItems {
		return fmt.Errorf("todo state exceeds %d items", maxItems)
	}
	seen := make(map[string]struct{}, len(s.Items))
	for index, item := range s.Items {
		if strings.TrimSpace(item.ID) == "" || item.ID != strings.TrimSpace(item.ID) {
			return fmt.Errorf("todo item %d id is invalid", index)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate todo item id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("todo item %q title is invalid", item.ID)
		}
		if maxTitleBytes > 0 && len(item.Title) > maxTitleBytes {
			return fmt.Errorf("todo item %q title exceeds %d bytes", item.ID, maxTitleBytes)
		}
	}
	return nil
}

func (s *TodoState) indexOf(id string) int {
	for index, item := range s.Items {
		if item.ID == id {
			return index
		}
	}
	return -1
}

func (s *TodoState) nextAvailableID() string {
	if s.NextID < 1 {
		s.NextID = 1
	}
	for {
		candidate := fmt.Sprintf("todo_%d", s.NextID)
		s.NextID++
		if s.indexOf(candidate) < 0 {
			return candidate
		}
	}
}

func countTodoDone(items []TodoItem) int {
	count := 0
	for _, item := range items {
		if item.Done {
			count++
		}
	}
	return count
}

func renderTodoText(summary string, items []TodoItem) string {
	var b strings.Builder
	b.WriteString(summary)
	if len(items) == 0 {
		return b.String()
	}
	b.WriteString("\nCurrent items:")
	for _, item := range items {
		status := " "
		if item.Done {
			status = "x"
		}
		fmt.Fprintf(&b, "\n- [%s] %s (%s)", status, item.Title, item.ID)
	}
	return b.String()
}
