package strictjson

import (
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguousJSON(t *testing.T) {
	t.Parallel()
	type value struct {
		Name   string `json:"name"`
		Nested struct {
			Value int `json:"value"`
		} `json:"nested"`
	}
	for _, test := range []struct {
		name string
		json string
	}{
		{name: "unknown field", json: `{"name":"one","extra":true}`},
		{name: "duplicate root key", json: `{"name":"one","name":"two"}`},
		{name: "duplicate nested key", json: `{"nested":{"value":1,"value":2}}`},
		{name: "duplicate key in array", json: `{"name":"one","nested":{"value":1},"extra":[{"id":1,"id":2}]}`},
		{name: "trailing value", json: `{"name":"one"} {}`},
		{name: "truncated", json: `{"name":"one"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var decoded value
			if err := Decode(strings.NewReader(test.json), &decoded); err == nil {
				t.Fatalf("Decode accepted %s", test.json)
			}
		})
	}
}

func TestDecodeAcceptsOneStrictValue(t *testing.T) {
	t.Parallel()
	var decoded struct {
		Name string `json:"name"`
	}
	if err := DecodeBytes([]byte(" \n {\"name\":\"one\"} \t"), &decoded); err != nil || decoded.Name != "one" {
		t.Fatalf("decoded = %#v, err = %v", decoded, err)
	}
}
