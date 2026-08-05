// Package openai implements the OpenAI-compatible Chat Completions protocol
// as an optional model.Model adapter.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

const (
	maximumCredentialBytes   = 16 << 10
	maximumErrorPayloadBytes = 4 << 10
)

var runtimeParameterFields = map[string]struct{}{
	"model": {}, "messages": {}, "tools": {},
	"max_tokens": {}, "max_completion_tokens": {}, "n": {},
	"stream": {}, "stream_options": {},
}

// Config binds an Adapter to one OpenAI-compatible endpoint. The caller owns
// Client and its transports, timeouts, idle connections, and shutdown.
type Config struct {
	// BaseURL is the API root, for example https://provider.example/v1.
	BaseURL string
	// APIKey is sent only in the Authorization header.
	APIKey string
	// Client is required and remains owned by the caller.
	Client *http.Client
	// AllowHTTP permits a trusted non-TLS endpoint. HTTPS is the default.
	AllowHTTP bool
}

// Adapter maps Tiggy model requests to the OpenAI-compatible streaming Chat
// Completions protocol. It is safe for concurrent use when Config.Client is.
type Adapter struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

var _ model.Model = (*Adapter)(nil)

// New validates Config without making a network request.
func New(config Config) (*Adapter, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" || len(baseURL) > 4096 {
		return nil, fmt.Errorf("OpenAI-compatible BaseURL is required and must be bounded")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, fmt.Errorf("OpenAI-compatible BaseURL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("OpenAI-compatible BaseURL cannot contain credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !config.AllowHTTP {
		return nil, fmt.Errorf("OpenAI-compatible BaseURL requires HTTPS unless AllowHTTP is set")
	}
	if strings.TrimSpace(config.APIKey) == "" || len(config.APIKey) > maximumCredentialBytes || strings.ContainsAny(config.APIKey, "\r\n") {
		return nil, fmt.Errorf("OpenAI-compatible APIKey is required and invalid")
	}
	if config.Client == nil {
		return nil, fmt.Errorf("OpenAI-compatible HTTP Client is required")
	}
	return &Adapter{endpoint: baseURL + "/chat/completions", apiKey: config.APIKey, client: config.Client}, nil
}

// Generate performs one streaming Chat Completions request. Per-call response
// bodies are always closed before return; the injected Client remains owned by
// the caller.
func (a *Adapter) Generate(ctx context.Context, request model.Request, sink model.DeltaSink) (model.Response, error) {
	if a == nil || a.client == nil {
		return model.Response{}, invalidRequestError("invalid_adapter", "The Provider adapter is not initialized.", nil)
	}
	if ctx == nil {
		return model.Response{}, invalidRequestError("invalid_context", "The Provider request requires a Context.", nil)
	}
	if err := request.Validate(); err != nil {
		return model.Response{}, invalidRequestError("invalid_runtime_request", "The Runtime model request is invalid.", err)
	}
	body, err := encodeRequest(request)
	if err != nil {
		return model.Response{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return model.Response{}, invalidRequestError("request_build_failed", "The Provider request could not be created.", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+a.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")

	httpResponse, err := a.client.Do(httpRequest)
	if err != nil {
		return model.Response{}, transportError(ctx, err)
	}
	defer httpResponse.Body.Close()
	requestID := firstHeader(httpResponse.Header, "x-request-id", "request-id")
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		metadata := readErrorMetadata(httpResponse.Body)
		providerError := statusError(httpResponse.StatusCode)
		classifyErrorMetadata(providerError, metadata)
		applyRetryAfter(providerError, httpResponse.Header.Get("Retry-After"))
		providerError.RequestID = requestID
		return model.Response{}, providerError
	}
	response, err := decodeStreamForRequest(httpResponse.Body, sink, requestID)
	if err != nil {
		if ctx.Err() != nil {
			providerError := transportError(ctx, err)
			providerError.RequestID = requestID
			return model.Response{}, providerError
		}
		return model.Response{}, err
	}
	response.ProviderRequestID = requestID
	return response, nil
}

func encodeRequest(request model.Request) ([]byte, error) {
	messages, err := toWireMessages(request.Messages)
	if err != nil {
		return nil, invalidRequestError("unsupported_message", "The Provider adapter cannot encode one or more Messages.", err)
	}
	tools := make([]wireTool, len(request.Tools))
	for index, definition := range request.Tools {
		tools[index] = wireTool{Type: "function", Function: wireFunctionDefinition{
			Name: definition.Name, Description: definition.Description,
			Parameters: append(json.RawMessage(nil), definition.InputSchema...),
		}}
	}
	fields := make(map[string]json.RawMessage)
	if len(request.Parameters) > 0 {
		if err := json.Unmarshal(request.Parameters, &fields); err != nil || fields == nil {
			return nil, invalidRequestError("invalid_parameters", "The Provider parameters must be a JSON object.", err)
		}
	}
	for key := range fields {
		if _, reserved := runtimeParameterFields[key]; reserved {
			return nil, invalidRequestError("reserved_parameter", "Provider parameters cannot override Runtime-owned fields.", fmt.Errorf("reserved field %q", key))
		}
	}
	reserved := map[string]any{
		"model": request.Model.Model, "messages": messages,
		"max_tokens": request.MaxOutputTokens, "stream": true,
		"stream_options": wireStreamOptions{IncludeUsage: true},
	}
	if len(tools) > 0 {
		reserved["tools"] = tools
	}
	for key, value := range reserved {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, invalidRequestError("request_encode_failed", "The Provider request could not be encoded.", err)
		}
		fields[key] = encoded
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, invalidRequestError("request_encode_failed", "The Provider request could not be encoded.", err)
	}
	return encoded, nil
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string                 `json:"type"`
	Function wireFunctionDefinition `json:"function"`
}

type wireFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireFunctionCall `json:"function"`
}

type wireFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func toWireMessages(values []message.Message) ([]wireMessage, error) {
	result := make([]wireMessage, 0, len(values))
	for messageIndex, value := range values {
		switch value.Role {
		case message.RoleSystem, message.RoleUser:
			text, err := textContents(value.Content)
			if err != nil {
				return nil, fmt.Errorf("message %d: %w", messageIndex, err)
			}
			if text == "" {
				return nil, fmt.Errorf("message %d: text content is required", messageIndex)
			}
			result = append(result, wireMessage{Role: string(value.Role), Content: text})
		case message.RoleAssistant:
			wire := wireMessage{Role: string(value.Role)}
			for _, content := range value.Content {
				switch content.Type {
				case message.ContentText:
					wire.Content += content.Text
				case message.ContentThinking:
					// Provider reasoning blocks are not portable across requests.
				case message.ContentToolCall:
					call := content.ToolCall
					wire.ToolCalls = append(wire.ToolCalls, wireToolCall{
						ID: call.ID, Type: "function",
						Function: wireFunctionCall{Name: call.Name, Arguments: string(call.Arguments)},
					})
				default:
					return nil, fmt.Errorf("message %d contains unsupported Assistant content %q", messageIndex, content.Type)
				}
			}
			if wire.Content == "" && len(wire.ToolCalls) == 0 {
				return nil, fmt.Errorf("message %d has no portable Assistant content", messageIndex)
			}
			result = append(result, wire)
		case message.RoleTool:
			for _, content := range value.Content {
				if content.Type != message.ContentToolResult || content.ToolResult == nil {
					return nil, fmt.Errorf("message %d contains unsupported Tool content %q", messageIndex, content.Type)
				}
				text, err := textContents(content.ToolResult.Content)
				if err != nil {
					return nil, fmt.Errorf("message %d Tool Result: %w", messageIndex, err)
				}
				if text == "" {
					return nil, fmt.Errorf("message %d Tool Result: text content is required", messageIndex)
				}
				result = append(result, wireMessage{Role: string(message.RoleTool), Content: text, ToolCallID: content.ToolResult.CallID})
			}
		default:
			return nil, fmt.Errorf("message %d has unsupported role %q", messageIndex, value.Role)
		}
	}
	return result, nil
}

func textContents(contents []message.Content) (string, error) {
	var builder strings.Builder
	for _, content := range contents {
		switch content.Type {
		case message.ContentText:
			builder.WriteString(content.Text)
		default:
			return "", fmt.Errorf("unsupported content %q", content.Type)
		}
	}
	return builder.String(), nil
}

func invalidRequestError(code, safe string, cause error) *model.ProviderError {
	return &model.ProviderError{Class: model.ErrorInvalidRequest, Code: code, SafeDetail: safe, Cause: cause}
}

func transportError(ctx context.Context, cause error) *model.ProviderError {
	if ctx.Err() != nil {
		class := model.ErrorCanceled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = model.ErrorTimeout
		}
		return &model.ProviderError{Class: class, Code: "request_canceled", Retryable: class == model.ErrorTimeout, SafeDetail: "The Provider request was canceled.", Cause: ctx.Err()}
	}
	return &model.ProviderError{Class: model.ErrorNetwork, Code: "network_error", Retryable: true, SafeDetail: "The Provider could not be reached.", Cause: cause}
}

func statusError(status int) *model.ProviderError {
	providerError := &model.ProviderError{Code: "http_" + strconv.Itoa(status), SafeDetail: "The Provider rejected the request."}
	switch status {
	case http.StatusUnauthorized:
		providerError.Class = model.ErrorAuthentication
	case http.StatusForbidden:
		providerError.Class = model.ErrorPermission
	case http.StatusTooManyRequests:
		providerError.Class, providerError.Retryable = model.ErrorRateLimit, true
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		providerError.Class, providerError.Retryable = model.ErrorTimeout, true
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		providerError.Class = model.ErrorInvalidRequest
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		providerError.Class, providerError.Retryable = model.ErrorUnavailable, true
	default:
		if status >= 500 {
			providerError.Class, providerError.Retryable = model.ErrorServer, true
		} else {
			providerError.Class = model.ErrorUnknown
		}
	}
	return providerError
}

func applyRetryAfter(providerError *model.ProviderError, value string) {
	if providerError == nil || !providerError.Retryable {
		return
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		providerError.RetryAfter = time.Duration(seconds) * time.Second
	}
}

type errorMetadata struct {
	Code string
	Type string
}

// readErrorMetadata reads only bounded, structured classification fields. The
// Provider message is intentionally not extracted or retained.
func readErrorMetadata(reader io.Reader) errorMetadata {
	if reader == nil {
		return errorMetadata{}
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, maximumErrorPayloadBytes+1))
	if err != nil || len(encoded) > maximumErrorPayloadBytes {
		return errorMetadata{}
	}
	var envelope struct {
		Error struct {
			Code any    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(encoded, &envelope) != nil {
		return errorMetadata{}
	}
	return errorMetadata{Code: normalizedMetadataString(envelope.Error.Code), Type: normalizedMetadataString(envelope.Error.Type)}
}

func normalizedMetadataString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if len(text) > 128 {
		return ""
	}
	return text
}

func classifyErrorMetadata(providerError *model.ProviderError, metadata errorMetadata) {
	if providerError == nil {
		return
	}
	for _, value := range []string{metadata.Code, metadata.Type} {
		switch value {
		case "context_length_exceeded", "context_window_exceeded", "max_tokens_exceeded":
			setNonRetryableClassification(providerError, model.ErrorContextLength, value)
			return
		case "insufficient_quota", "quota_exceeded", "billing_hard_limit_reached":
			setNonRetryableClassification(providerError, model.ErrorQuota, value)
			return
		case "content_filter", "content_policy_violation", "safety_violation":
			setNonRetryableClassification(providerError, model.ErrorSafety, value)
			return
		case "authentication_error", "invalid_api_key":
			setNonRetryableClassification(providerError, model.ErrorAuthentication, value)
			return
		case "permission_denied":
			setNonRetryableClassification(providerError, model.ErrorPermission, value)
			return
		case "rate_limit_exceeded":
			providerError.Class, providerError.Code, providerError.Retryable = model.ErrorRateLimit, value, true
			return
		case "model_not_found":
			setNonRetryableClassification(providerError, model.ErrorUnavailable, value)
			return
		}
	}
}

func setNonRetryableClassification(providerError *model.ProviderError, class model.ErrorClass, code string) {
	providerError.Class = class
	providerError.Code = code
	providerError.Retryable = false
	providerError.RetryAfter = 0
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			if len(value) <= 1024 {
				return value
			}
			return ""
		}
	}
	return ""
}
