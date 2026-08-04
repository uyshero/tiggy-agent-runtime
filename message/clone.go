package message

import "encoding/json"

func Clone(value Message) Message {
	cloned := value
	cloned.Content = CloneContent(value.Content)
	cloned.Metadata = cloneRaw(value.Metadata)
	return cloned
}

func CloneAll(values []Message) []Message {
	if values == nil {
		return nil
	}
	cloned := make([]Message, len(values))
	for index, value := range values {
		cloned[index] = Clone(value)
	}
	return cloned
}

func CloneContent(values []Content) []Content {
	if values == nil {
		return nil
	}
	cloned := make([]Content, len(values))
	for index, part := range values {
		cloned[index] = part
		if part.Image != nil {
			image := *part.Image
			cloned[index].Image = &image
		}
		if part.Thinking != nil {
			thinking := *part.Thinking
			cloned[index].Thinking = &thinking
		}
		if part.ToolCall != nil {
			call := *part.ToolCall
			call.Arguments = cloneRaw(call.Arguments)
			cloned[index].ToolCall = &call
		}
		if part.ToolResult != nil {
			result := *part.ToolResult
			result.Content = CloneContent(result.Content)
			result.State = cloneRaw(result.State)
			cloned[index].ToolResult = &result
		}
		if part.Artifact != nil {
			artifact := *part.Artifact
			cloned[index].Artifact = &artifact
		}
	}
	return cloned
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
