package agentcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeArguments strictly decodes one JSON object into target. Unknown
// fields and trailing JSON values are rejected so raw Tool handlers can share
// the same input-safety behavior.
func DecodeArguments(arguments json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("decode tool arguments: expected a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode tool arguments: multiple JSON values")
		}
		return fmt.Errorf("decode tool arguments: %w", err)
	}
	return nil
}
