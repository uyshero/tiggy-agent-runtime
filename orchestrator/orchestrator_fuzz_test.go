package orchestrator

import (
	"bytes"
	"testing"
)

func FuzzReadBrief(f *testing.F) {
	for _, seed := range []string{
		`{"id":"brief","goal":"goal","tasks":[{"id":"one","objective":"work","subagent":"worker","output":{"name":"one.json","media_type":"application/json"}}]}`,
		`{"id":"brief","id":"changed","goal":"goal","tasks":[]}`,
		`{"id":"brief","goal":"goal","tasks":[{"id":"one","objective":"work","subagent":"worker","output":{"name":"one.json","name":"two.json","media_type":"application/json"}}]}`,
		`{}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		brief, err := ReadBrief(bytes.NewReader(data))
		if err != nil {
			return
		}
		if err := brief.Validate(); err != nil {
			t.Fatalf("ReadBrief returned an invalid Brief: %v", err)
		}
		if _, err := brief.Digest(); err != nil {
			t.Fatalf("digest accepted Brief: %v", err)
		}
	})
}
