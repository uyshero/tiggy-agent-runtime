package artifact_test

import (
	"encoding/json"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/artifact"
)

func FuzzDecodeToolResult(f *testing.F) {
	for _, seed := range []string{
		`{"protocol_version":"tiggy.artifact.tool-result.v1","result":{"call_id":"call","name":"tool","content":[{"type":"text","text":"ok"}]}}`,
		`{"protocol_version":"tiggy.artifact.tool-result.v1","protocol_version":"changed","result":{}}`,
		`{"protocol_version":"tiggy.artifact.tool-result.v1","result":{"call_id":"call","name":"tool","content":[{"type":"text","text":"ok"}],"state":{"x":1,"x":2}}}`,
		`null`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		result, err := artifact.DecodeToolResult(payload)
		if err != nil {
			return
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("DecodeToolResult returned an invalid result: %v", err)
		}
		if _, err := json.Marshal(result); err != nil {
			t.Fatalf("marshal accepted result: %v", err)
		}
	})
}
