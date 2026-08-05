package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestNewValidatesConfiguration(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	})}
	for _, test := range []struct {
		name   string
		config Config
		valid  bool
	}{
		{name: "valid HTTPS", config: Config{BaseURL: "https://provider.example/v1/", APIKey: "secret", Client: client}, valid: true},
		{name: "trusted HTTP", config: Config{BaseURL: "http://localhost:8080/v1", APIKey: "secret", Client: client, AllowHTTP: true}, valid: true},
		{name: "missing URL", config: Config{APIKey: "secret", Client: client}},
		{name: "HTTP rejected", config: Config{BaseURL: "http://localhost/v1", APIKey: "secret", Client: client}},
		{name: "URL credentials rejected", config: Config{BaseURL: "https://user@provider.example/v1", APIKey: "secret", Client: client}},
		{name: "missing key", config: Config{BaseURL: "https://provider.example/v1", Client: client}},
		{name: "header injection rejected", config: Config{BaseURL: "https://provider.example/v1", APIKey: "bad\nkey", Client: client}},
		{name: "missing Client", config: Config{BaseURL: "https://provider.example/v1", APIKey: "secret"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			adapter, err := New(test.config)
			if test.valid && (err != nil || adapter == nil) {
				t.Fatalf("New() = %v, %v", adapter, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("invalid Config was accepted: %#v", test.config)
			}
		})
	}
}

func TestGenerateMapsMessagesToolsAndParameters(t *testing.T) {
	t.Parallel()

	var captured []byte
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://provider.example/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Accept") != "text/event-stream" {
			return nil, errors.New("unexpected HTTP request")
		}
		var err error
		captured, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"total_tokens\":11}}\n\n" +
			"data: [DONE]\n\n"
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": []string{"request-1"}},
			Body: io.NopCloser(strings.NewReader(stream)),
		}, nil
	})
	adapter, err := New(Config{
		BaseURL: "https://provider.example/v1", APIKey: "secret",
		Client: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		RunID: "run-1", CallID: "model-1", Purpose: model.PurposeAgent,
		Model: model.Identity{Provider: "openai-compatible", Model: "test-model"},
		Messages: []message.Message{
			textMessage("system-1", message.RoleSystem, message.VisibilityInternal, "system"),
			textMessage("user-1", message.RoleUser, message.VisibilityPublic, "question"),
			{
				ID: "assistant-1", Role: message.RoleAssistant, Visibility: message.VisibilityPublic,
				Content: []message.Content{{Type: message.ContentToolCall, ToolCall: &message.ToolCall{
					ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"id":"42"}`),
				}}},
			},
			{
				ID: "tool-1", Role: message.RoleTool, Visibility: message.VisibilityInternal,
				Content: []message.Content{{Type: message.ContentToolResult, ToolResult: &message.ToolResult{
					CallID: "call-1", Name: "lookup", Content: []message.Content{{Type: message.ContentText, Text: "found"}},
				}}},
			},
		},
		Tools: []model.ToolDefinition{{
			Name: "lookup", Description: "Look up a record.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		}},
		MaxOutputTokens: 64, Parameters: json.RawMessage(`{"temperature":0.1}`),
	}
	response, err := adapter.Generate(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.ProviderRequestID != "request-1" || response.StopReason != model.StopReasonComplete || response.Usage.TotalTokens != 11 {
		t.Fatalf("response = %#v", response)
	}
	var wire struct {
		Model         string            `json:"model"`
		Messages      []wireMessage     `json:"messages"`
		Tools         []wireTool        `json:"tools"`
		Temperature   float64           `json:"temperature"`
		Stream        bool              `json:"stream"`
		StreamOptions wireStreamOptions `json:"stream_options"`
	}
	if err := json.Unmarshal(captured, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != "test-model" || len(wire.Messages) != 4 || len(wire.Tools) != 1 || wire.Temperature != 0.1 || !wire.Stream || !wire.StreamOptions.IncludeUsage {
		t.Fatalf("wire request = %#v", wire)
	}
	if len(wire.Messages[2].ToolCalls) != 1 || wire.Messages[2].ToolCalls[0].Function.Name != "lookup" || wire.Messages[3].ToolCallID != "call-1" {
		t.Fatalf("wire tool history = %#v", wire.Messages)
	}
	if wire.Tools[0].Function.Name != "lookup" || !json.Valid(wire.Tools[0].Function.Parameters) {
		t.Fatalf("wire tools = %#v", wire.Tools)
	}
}

func TestDecodeStreamAssemblesToolCall(t *testing.T) {
	t.Parallel()

	stream := strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"brief\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"id\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"42\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n")
	var deltas []model.Delta
	response, err := decodeStream(stream, func(delta model.Delta) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != model.StopReasonToolCall || response.Usage.TotalTokens != 12 || len(response.Message.Content) != 1 {
		t.Fatalf("response = %#v", response)
	}
	call := response.Message.Content[0].ToolCall
	if call == nil || call.ID != "call-1" || call.Name != "lookup" || string(call.Arguments) != `{"id":"42"}` || call.ArgumentsError != "" {
		t.Fatalf("Tool Call = %#v", call)
	}
	if len(deltas) != 5 || deltas[0].Type != model.DeltaThinking || deltas[1].Type != model.DeltaToolCall || deltas[2].Type != model.DeltaToolCall || deltas[3].Type != model.DeltaStopped || deltas[4].Type != model.DeltaUsage {
		t.Fatalf("Deltas = %#v", deltas)
	}
}

func TestDecodeStreamPreservesSinkError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("stop stream")
	_, err := decodeStream(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"text\"}}]}\n\n"), func(model.Delta) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want sink error", err)
	}
}

func TestDecodeStreamRejectsCleanTruncation(t *testing.T) {
	t.Parallel()

	_, err := decodeStream(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"), nil)
	var providerError *model.ProviderError
	if !errors.As(err, &providerError) || providerError.Class != model.ErrorStreamProtocol || providerError.Code != "incomplete_stream" {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeStreamRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		stream string
		code   string
	}{
		{
			name:   "negative Tool Call index",
			stream: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":-1,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			code:   "invalid_tool_call_index",
		},
		{
			name:   "unsupported Tool Call type",
			stream: `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"custom","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
			code:   "unsupported_tool_call_type",
		},
		{
			name: "conflicting finish reason",
			stream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n",
			code: "conflicting_finish_reason",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeStream(strings.NewReader(test.stream), nil)
			var providerError *model.ProviderError
			if !errors.As(err, &providerError) || providerError.Class != model.ErrorStreamProtocol || providerError.Code != test.code {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDecodeStreamClassifiesProviderFinishFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		finish string
		class  model.ErrorClass
		code   string
	}{
		{finish: "content_filter", class: model.ErrorSafety, code: "content_filter"},
		{finish: "canceled", class: model.ErrorCanceled, code: "provider_canceled"},
		{finish: "error", class: model.ErrorUnknown, code: "generation_error"},
	} {
		test := test
		t.Run(test.finish, func(t *testing.T) {
			t.Parallel()
			stream := fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", test.finish)
			_, err := decodeStream(strings.NewReader(stream), nil)
			var providerError *model.ProviderError
			if !errors.As(err, &providerError) || providerError.Class != test.class || providerError.Code != test.code {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGenerateRejectsReservedParametersBeforeHTTP(t *testing.T) {
	t.Parallel()

	calls := 0
	adapter, err := New(Config{
		BaseURL: "https://provider.example/v1", APIKey: "secret",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("must not be called")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, parameters := range []json.RawMessage{
		json.RawMessage(`{"model":"override"}`),
		json.RawMessage(`{"max_completion_tokens":1024}`),
		json.RawMessage(`{"n":2}`),
		json.RawMessage(`{"tools":[]}`),
	} {
		request := model.Request{
			RunID: "run-1", CallID: "model-1", Purpose: model.PurposeAgent,
			Model:           model.Identity{Provider: "fixture", Model: "model"},
			Messages:        []message.Message{textMessage("user-1", message.RoleUser, message.VisibilityPublic, "question")},
			MaxOutputTokens: 16, Parameters: parameters,
		}
		_, generateErr := adapter.Generate(context.Background(), request, nil)
		var providerError *model.ProviderError
		if !errors.As(generateErr, &providerError) || providerError.Class != model.ErrorInvalidRequest || providerError.Code != "reserved_parameter" {
			t.Fatalf("parameters %s: error = %v", parameters, generateErr)
		}
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
}

func TestGenerateClassifiesBoundedErrorMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		status     int
		body       string
		wantClass  model.ErrorClass
		wantCode   string
		retryable  bool
		retryAfter string
		wantDelay  time.Duration
	}{
		{
			name: "context length", status: http.StatusBadRequest,
			body:      `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"private prompt detail"}}`,
			wantClass: model.ErrorContextLength, wantCode: "context_length_exceeded",
		},
		{
			name: "quota", status: http.StatusTooManyRequests,
			body:      `{"error":{"type":"insufficient_quota","message":"private billing detail"}}`,
			wantClass: model.ErrorQuota, wantCode: "insufficient_quota", retryAfter: "30",
		},
		{
			name: "rate limit metadata applies retry after", status: http.StatusBadRequest,
			body:      `{"error":{"code":"rate_limit_exceeded"}}`,
			wantClass: model.ErrorRateLimit, wantCode: "rate_limit_exceeded",
			retryable: true, retryAfter: "3", wantDelay: 3 * time.Second,
		},
		{
			name: "unknown metadata falls back to status", status: http.StatusBadRequest,
			body:      `{"error":{"code":"provider_private_code","message":"private provider detail"}}`,
			wantClass: model.ErrorInvalidRequest, wantCode: "http_400",
		},
		{
			name: "malformed envelope falls back to status", status: http.StatusBadRequest,
			body: `{"error":`, wantClass: model.ErrorInvalidRequest, wantCode: "http_400",
		},
		{
			name: "oversized envelope falls back to status", status: http.StatusBadRequest,
			body:      strings.Repeat("x", maximumErrorPayloadBytes+1),
			wantClass: model.ErrorInvalidRequest, wantCode: "http_400",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			header := http.Header{"X-Request-Id": []string{"request-failure-1"}}
			if test.retryAfter != "" {
				header.Set("Retry-After", test.retryAfter)
			}
			adapter, err := New(Config{
				BaseURL: "https://provider.example/v1", APIKey: "secret",
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: test.status,
						Header:     header,
						Body:       io.NopCloser(strings.NewReader(test.body)),
					}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Generate(context.Background(), validRequest(), nil)
			var providerError *model.ProviderError
			if !errors.As(err, &providerError) || providerError.Class != test.wantClass || providerError.Code != test.wantCode || providerError.Retryable != test.retryable {
				t.Fatalf("error = %+v", providerError)
			}
			if providerError.RequestID != "request-failure-1" {
				t.Fatalf("RequestID = %q", providerError.RequestID)
			}
			if providerError.RetryAfter != test.wantDelay {
				t.Fatalf("RetryAfter = %s, want %s", providerError.RetryAfter, test.wantDelay)
			}
			if strings.Contains(providerError.Error(), "private") {
				t.Fatalf("Provider Error leaked response message: %v", providerError)
			}
		})
	}
}

func TestDecodeStreamClassifiesOnlyAllowlistedErrorMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		body      string
		wantClass model.ErrorClass
		wantCode  string
	}{
		{
			name:      "known context error",
			body:      `data: {"error":{"type":"invalid_request_error","code":"context_length_exceeded"}}` + "\n\n",
			wantClass: model.ErrorContextLength, wantCode: "context_length_exceeded",
		},
		{
			name:      "unknown code is not exposed",
			body:      `data: {"error":{"type":"unknown","code":"private_error_detail"}}` + "\n\n",
			wantClass: model.ErrorUnknown, wantCode: "stream_error",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeStream(strings.NewReader(test.body), nil)
			var providerError *model.ProviderError
			if !errors.As(err, &providerError) || providerError.Class != test.wantClass || providerError.Code != test.wantCode {
				t.Fatalf("error = %+v", providerError)
			}
			if strings.Contains(providerError.Error(), "private") {
				t.Fatalf("Provider Error leaked unknown metadata: %v", providerError)
			}
		})
	}
}

func TestGenerateAddsRequestIDOnlyToProviderStreamFailures(t *testing.T) {
	t.Parallel()

	response := func(stream string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"X-Request-Id": []string{"request-stream-1"}},
			Body:       io.NopCloser(strings.NewReader(stream)),
		}
	}
	adapter, err := New(Config{
		BaseURL: "https://provider.example/v1", APIKey: "secret",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response("data: {not-json}\n\n"), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Generate(context.Background(), validRequest(), nil)
	var providerError *model.ProviderError
	if !errors.As(err, &providerError) || providerError.RequestID != "request-stream-1" {
		t.Fatalf("error = %+v", providerError)
	}

	sentinel := &model.ProviderError{Class: model.ErrorUnknown, Code: "sink_owned"}
	adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"text\"}}]}\n\n"), nil
	})
	_, err = adapter.Generate(context.Background(), validRequest(), func(model.Delta) error { return sentinel })
	if err != sentinel || sentinel.RequestID != "" {
		t.Fatalf("sink error = %+v, want unchanged sentinel", err)
	}
}

func TestAdapterCompletesRuntimeToolLoop(t *testing.T) {
	t.Parallel()

	httpCalls := 0
	toolCalls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		httpCalls++
		encoded, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		body := string(encoded)
		switch httpCalls {
		case 1:
			if !strings.Contains(body, `"tools":[`) || !strings.Contains(body, `"name":"lookup"`) {
				return nil, errors.New("first request did not expose the Toolkit")
			}
			stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n"
			return successfulResponse(stream), nil
		case 2:
			for _, required := range []string{`"role":"tool"`, `"tool_call_id":"call-1"`, `"content":"tool-result"`} {
				if !strings.Contains(body, required) {
					return nil, errors.New("second request did not contain the Tool Result")
				}
			}
			stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"completed\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n"
			return successfulResponse(stream), nil
		default:
			return nil, errors.New("unexpected implicit Provider replay")
		}
	})
	adapter, err := New(Config{
		BaseURL: "https://provider.example/v1", APIKey: "secret",
		Client: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	kit, err := toolkit.New(agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{
			Name: "lookup", Description: "Return one deterministic result.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
		ExecuteFunc: func(context.Context, json.RawMessage) (tool.Result, error) {
			toolCalls++
			return tool.TextResult("tool-result"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.New(agent.Config{
		Name: "openai-tool-loop", Model: adapter,
		ModelIdentity: model.Identity{Provider: "fixture", Model: "model"},
		Toolkit:       kit, IDs: agenttest.SequentialIDs(),
		Now: agenttest.StepClock(time.Unix(1_700_000_000, 0).UTC(), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(context.Background(), message.Message{
		Role: message.RoleUser, Visibility: message.VisibilityPublic,
		Content: []message.Content{{Type: message.ContentText, Text: "use the lookup tool"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != agent.StatusCompleted || result.Final == nil || result.Final.Content[0].Text != "completed" {
		t.Fatalf("result = %#v", result)
	}
	if httpCalls != 2 || toolCalls != 1 {
		t.Fatalf("calls = HTTP %d, Tool %d", httpCalls, toolCalls)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatal(err)
	}
}

func textMessage(id string, role message.Role, visibility message.Visibility, text string) message.Message {
	return message.Message{ID: id, Role: role, Visibility: visibility, Content: []message.Content{{Type: message.ContentText, Text: text}}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func successfulResponse(stream string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}
}

func validRequest() model.Request {
	return model.Request{
		RunID: "run-1", CallID: "model-1", Purpose: model.PurposeAgent,
		Model:           model.Identity{Provider: "fixture", Model: "model"},
		Messages:        []message.Message{textMessage("user-1", message.RoleUser, message.VisibilityPublic, "question")},
		MaxOutputTokens: 16,
	}
}
