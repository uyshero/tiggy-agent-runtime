package event_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

func TestProtocolV1JSONFixturesRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("message", func(t *testing.T) {
		roundTripFixture[message.Message](t, "message-tool-call.json", func(value message.Message) error {
			return value.Validate()
		})
	})
	t.Run("artifact message", func(t *testing.T) {
		roundTripFixture[message.Message](t, "message-artifact-result.json", func(value message.Message) error {
			return value.Validate()
		})
	})
	t.Run("model request", func(t *testing.T) {
		roundTripFixture[model.Request](t, "model-request.json", func(value model.Request) error {
			return value.Validate()
		})
	})
	t.Run("model response", func(t *testing.T) {
		roundTripFixture[model.Response](t, "model-response.json", func(value model.Response) error {
			return value.Validate()
		})
	})
	t.Run("event", func(t *testing.T) {
		roundTripFixture[event.Event](t, "event-retry-scheduled.json", func(value event.Event) error {
			return value.Validate()
		})
	})
	t.Run("model call context event", func(t *testing.T) {
		value := roundTripFixture[event.Event](t, "event-model-call-started-context.json", func(value event.Event) error {
			return value.Validate()
		})
		payload := decodeModelCallPayload(t, value)
		if payload.Context == nil {
			t.Fatal("model Call context fixture omitted Context Manifest")
		}
		if err := payload.Context.Validate(); err != nil {
			t.Fatalf("validate Context Manifest: %v", err)
		}
	})
	t.Run("legacy model call event", func(t *testing.T) {
		value := roundTripFixture[event.Event](t, "event-model-call-started-legacy.json", func(value event.Event) error {
			return value.Validate()
		})
		if payload := decodeModelCallPayload(t, value); payload.Context != nil {
			t.Fatal("legacy model Call fixture unexpectedly contains Context Manifest")
		}
	})
}

func roundTripFixture[T any](t *testing.T, name string, validate func(T) error) T {
	t.Helper()
	path := filepath.Join("..", "testdata", "protocol", "v1", name)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var first T
	if err := json.Unmarshal(original, &first); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if err := validate(first); err != nil {
		t.Fatalf("validate fixture: %v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var second T
	if err := json.Unmarshal(encoded, &second); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if err := validate(second); err != nil {
		t.Fatalf("validate round trip: %v", err)
	}
	assertJSONEqual(t, original, encoded)
	return second
}

func decodeModelCallPayload(t *testing.T, value event.Event) event.ModelCallPayload {
	t.Helper()
	if value.Type != event.ModelCallStarted {
		t.Fatalf("Event type = %q, want %q", value.Type, event.ModelCallStarted)
	}
	var payload event.ModelCallPayload
	if err := json.Unmarshal(value.Payload, &payload); err != nil {
		t.Fatalf("decode Model Call payload: %v", err)
	}
	return payload
}

func assertJSONEqual(t *testing.T, first, second []byte) {
	t.Helper()
	var firstValue any
	var secondValue any
	if err := json.Unmarshal(first, &firstValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &secondValue); err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(firstValue)
	secondJSON, _ := json.Marshal(secondValue)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("JSON changed after round trip\noriginal: %s\nencoded:  %s", firstJSON, secondJSON)
	}
}
