package strictjson

import (
	"encoding/json"
	"testing"
)

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{
		`{"name":"one","nested":{"value":1}}`,
		`{"name":"one","name":"two"}`,
		`{"nested":{"value":1,"value":2}}`,
		`{"name":"one"} {}`,
		`null`,
	} {
		f.Add([]byte(seed))
	}
	type value struct {
		Name   string `json:"name"`
		Nested struct {
			Value int `json:"value"`
		} `json:"nested"`
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var decoded value
		if err := DecodeBytes(data, &decoded); err != nil {
			return
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("marshal accepted value: %v", err)
		}
		var roundTrip value
		if err := DecodeBytes(encoded, &roundTrip); err != nil {
			t.Fatalf("decode accepted round trip: %v", err)
		}
	})
}
