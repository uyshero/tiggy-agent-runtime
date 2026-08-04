package message

import "encoding/json"

// Clone 深拷贝单条 Message（含 Content 与 Metadata 缓冲）。
func Clone(value Message) Message {
	cloned := value
	cloned.Content = CloneContent(value.Content)
	cloned.Metadata = cloneRaw(value.Metadata)
	return cloned
}

// CloneAll 深拷贝消息切片；nil 输入返回 nil。
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

// CloneContent 深拷贝内容块及其嵌套指针/RawMessage，避免共享可变缓冲。
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
