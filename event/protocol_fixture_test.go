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
}

func roundTripFixture[T any](t *testing.T, name string, validate func(T) error) {
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
