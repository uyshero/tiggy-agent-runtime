package openai

import (
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/message"
)

func FuzzDecodeStream(f *testing.F) {
	for _, seed := range []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call\",\"type\":\"function\",\"function\":{\"name\":\"tool\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"data: {not-json}\n\n",
		"data: [DONE]\n\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, stream string) {
		response, err := decodeStream(strings.NewReader(stream), nil)
		if err != nil {
			return
		}
		response.Message.ID = "fuzz-message"
		response.Message.Role = message.RoleAssistant
		response.Message.Visibility = message.VisibilityPublic
		if err := response.Validate(); err != nil {
			t.Fatalf("decodeStream returned an invalid response: %v", err)
		}
	})
}
