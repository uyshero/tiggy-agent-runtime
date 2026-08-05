package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

const maximumStreamLineBytes = 1 << 20

type streamUsage struct {
	PromptTokens       int64 `json:"prompt_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	PromptTokenDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type streamChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *streamUsage `json:"usage"`
	Error *struct {
		Type string `json:"type"`
		Code any    `json:"code"`
	} `json:"error"`
}

type toolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func decodeStream(reader io.Reader, sink model.DeltaSink) (model.Response, error) {
	return decodeStreamForRequest(reader, sink, "")
}

func decodeStreamForRequest(reader io.Reader, sink model.DeltaSink, requestID string) (response model.Response, err error) {
	defer func() {
		var sinkFailure *deltaSinkFailure
		if errors.As(err, &sinkFailure) {
			err = sinkFailure.cause
			return
		}
		var providerError *model.ProviderError
		if requestID != "" && errors.As(err, &providerError) {
			providerError.RequestID = requestID
		}
	}()
	if reader == nil {
		return model.Response{}, streamError("invalid_stream", "The Provider returned an invalid stream.", nil)
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), maximumStreamLineBytes)
	var textBuilder strings.Builder
	var usage model.Usage
	stopReason := model.StopReasonComplete
	sawFinish := false
	toolCalls := make(map[int]*toolCallBuilder)
	emit := func(delta model.Delta) error {
		if sink == nil {
			return nil
		}
		if err := sink(delta); err != nil {
			return &deltaSinkFailure{cause: err}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return model.Response{}, streamError("invalid_stream_chunk", "The Provider returned an invalid stream chunk.", err)
		}
		if chunk.Error != nil {
			providerError := &model.ProviderError{
				Class: model.ErrorUnknown, Code: "stream_error", SafeDetail: "The Provider returned a stream error.",
			}
			classifyErrorMetadata(providerError, errorMetadata{
				Code: normalizedMetadataString(chunk.Error.Code),
				Type: normalizedMetadataString(chunk.Error.Type),
			})
			return model.Response{}, providerError
		}
		if chunk.Usage != nil {
			usage = model.Usage{
				InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens: chunk.Usage.TotalTokens, CachedInputTokens: chunk.Usage.PromptTokenDetails.CachedTokens,
				ReasoningTokens: chunk.Usage.CompletionDetails.ReasoningTokens, Source: model.UsageSourceProvider,
			}
			if err := usage.Validate(); err != nil {
				return model.Response{}, streamError("invalid_usage", "The Provider returned invalid Usage.", err)
			}
			usageCopy := usage
			if err := emit(model.Delta{Type: model.DeltaUsage, Usage: &usageCopy}); err != nil {
				return model.Response{}, err
			}
		}
		if len(chunk.Choices) > 1 {
			return model.Response{}, streamError("multiple_choices", "The Provider returned multiple choices for one Agent call.", nil)
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				return model.Response{}, streamError("unsupported_choice", "The Provider returned an unsupported choice index.", nil)
			}
			if choice.Delta.ReasoningContent != "" {
				if err := emit(model.Delta{Type: model.DeltaThinking, Index: 0, Text: choice.Delta.ReasoningContent}); err != nil {
					return model.Response{}, err
				}
			}
			content, err := contentText(choice.Delta.Content)
			if err != nil {
				return model.Response{}, streamError("invalid_content_delta", "The Provider returned an invalid content Delta.", err)
			}
			if content != "" {
				textBuilder.WriteString(content)
				if err := emit(model.Delta{Type: model.DeltaText, Index: 0, Text: content}); err != nil {
					return model.Response{}, err
				}
			}
			for _, fragment := range choice.Delta.ToolCalls {
				if fragment.Index < 0 {
					return model.Response{}, streamError("invalid_tool_call_index", "The Provider returned an invalid Tool Call index.", nil)
				}
				if fragment.Type != "" && fragment.Type != "function" {
					return model.Response{}, streamError("unsupported_tool_call_type", "The Provider returned an unsupported Tool Call type.", nil)
				}
				builder := toolCalls[fragment.Index]
				if builder == nil {
					builder = &toolCallBuilder{}
					toolCalls[fragment.Index] = builder
				}
				if fragment.ID != "" {
					if builder.id != "" && builder.id != fragment.ID {
						return model.Response{}, streamError("conflicting_tool_call", "The Provider changed a Tool Call identifier during streaming.", nil)
					}
					builder.id = fragment.ID
				}
				if fragment.Function.Name != "" {
					if builder.name != "" && builder.name != fragment.Function.Name {
						return model.Response{}, streamError("conflicting_tool_call", "The Provider changed a Tool name during streaming.", nil)
					}
					builder.name = fragment.Function.Name
				}
				builder.arguments.WriteString(fragment.Function.Arguments)
				delta := model.ToolCallDelta{
					Index: fragment.Index, ID: fragment.ID, Name: fragment.Function.Name,
					ArgumentsFragment: fragment.Function.Arguments,
				}
				if err := emit(model.Delta{Type: model.DeltaToolCall, Index: fragment.Index, ToolCall: &delta}); err != nil {
					return model.Response{}, err
				}
			}
			if choice.FinishReason != "" {
				mapped, err := stopReasonFromWire(choice.FinishReason)
				if err != nil {
					return model.Response{}, err
				}
				if sawFinish && stopReason != mapped {
					return model.Response{}, streamError("conflicting_finish_reason", "The Provider changed its finish reason during streaming.", nil)
				}
				stopReason = mapped
				sawFinish = true
				if err := emit(model.Delta{Type: model.DeltaStopped, Index: 0, StopReason: stopReason}); err != nil {
					return model.Response{}, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return model.Response{}, &model.ProviderError{
			Class: model.ErrorNetwork, Code: "stream_read_failed", Retryable: true,
			SafeDetail: "The Provider stream could not be read.", Cause: err,
		}
	}
	if !sawFinish {
		return model.Response{}, streamError("incomplete_stream", "The Provider stream ended without a finish reason.", nil)
	}
	contents := make([]message.Content, 0, len(toolCalls)+1)
	if textBuilder.Len() > 0 {
		contents = append(contents, message.Content{Type: message.ContentText, Text: textBuilder.String()})
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		builder := toolCalls[index]
		if strings.TrimSpace(builder.id) == "" || strings.TrimSpace(builder.name) == "" {
			return model.Response{}, streamError("invalid_tool_call", "The Provider returned an incomplete Tool Call.", nil)
		}
		arguments := []byte(builder.arguments.String())
		argumentsError := ""
		if len(bytes.TrimSpace(arguments)) == 0 {
			arguments = []byte(`{}`)
		} else if !json.Valid(arguments) || bytes.TrimSpace(arguments)[0] != '{' {
			arguments = []byte(`{}`)
			argumentsError = "The Provider returned invalid Tool arguments."
		}
		contents = append(contents, message.Content{Type: message.ContentToolCall, ToolCall: &message.ToolCall{
			ID: builder.id, Name: builder.name, Arguments: arguments, ArgumentsError: argumentsError,
		}})
	}
	if len(contents) == 0 {
		return model.Response{}, streamError("empty_stream", "The Provider stream contained no final content.", nil)
	}
	if len(toolCalls) > 0 && stopReason != model.StopReasonToolCall {
		return model.Response{}, streamError("tool_call_stop_mismatch", "The Provider returned Tool Calls with an incompatible finish reason.", nil)
	}
	if len(toolCalls) == 0 && stopReason == model.StopReasonToolCall {
		return model.Response{}, streamError("missing_tool_call", "The Provider finished for Tool Calls without returning one.", nil)
	}
	return model.Response{Message: message.Message{Content: contents}, StopReason: stopReason, Usage: usage}, nil
}

type deltaSinkFailure struct {
	cause error
}

func (e *deltaSinkFailure) Error() string {
	return e.cause.Error()
}

func (e *deltaSinkFailure) Unwrap() error {
	return e.cause
}

func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" || part.Type == "output_text" || part.Type == "" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String(), nil
}

func stopReasonFromWire(value string) (model.StopReason, error) {
	switch value {
	case "stop":
		return model.StopReasonComplete, nil
	case "tool_calls", "function_call":
		return model.StopReasonToolCall, nil
	case "length":
		return model.StopReasonLength, nil
	case "cancelled", "canceled":
		return "", &model.ProviderError{
			Class: model.ErrorCanceled, Code: "provider_canceled",
			SafeDetail: "The Provider canceled generation.",
		}
	case "content_filter":
		return "", &model.ProviderError{
			Class: model.ErrorSafety, Code: "content_filter",
			SafeDetail: "The Provider blocked generation under its safety policy.",
		}
	case "error":
		return "", &model.ProviderError{
			Class: model.ErrorUnknown, Code: "generation_error",
			SafeDetail: "The Provider failed during generation.",
		}
	default:
		return "", streamError("unknown_finish_reason", "The Provider returned an unknown finish reason.", nil)
	}
}

func streamError(code, safe string, cause error) *model.ProviderError {
	return &model.ProviderError{Class: model.ErrorStreamProtocol, Code: code, SafeDetail: safe, Cause: cause}
}
