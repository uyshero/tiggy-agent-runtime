// Package strictjson provides the shared decoder for closed Runtime JSON
// contracts. It is internal so strictness can evolve without adding public API.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeBytes decodes exactly one JSON value, rejecting unknown struct fields
// and duplicate object keys at every nesting level.
func DecodeBytes(data []byte, destination any) error {
	return Decode(bytes.NewReader(data), destination)
}

// Decode decodes exactly one JSON value, rejecting unknown struct fields and
// duplicate object keys at every nesting level.
func Decode(reader io.Reader, destination any) error {
	if reader == nil {
		return fmt.Errorf("JSON reader is required")
	}
	if destination == nil {
		return fmt.Errorf("JSON destination is required")
	}
	decoder := json.NewDecoder(reader)
	var data json.RawMessage
	if err := decoder.Decode(&data); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	if err := validateOneValue(data); err != nil {
		return err
	}
	valueDecoder := json.NewDecoder(bytes.NewReader(data))
	valueDecoder.DisallowUnknownFields()
	if err := valueDecoder.Decode(destination); err != nil {
		return err
	}
	return nil
}

func validateOneValue(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("invalid JSON object delimiter %q", closing)
		}
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("invalid JSON array delimiter %q", closing)
		}
	default:
		return fmt.Errorf("invalid JSON delimiter %q", delimiter)
	}
	return nil
}
