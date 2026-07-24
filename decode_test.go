package agentcli

import (
	"encoding/json"
	"testing"
)

func TestDecodeArgumentsStrictlyDecodesOneObject(t *testing.T) {
	var input struct {
		Message string `json:"message"`
	}
	if err := DecodeArguments(json.RawMessage(`{"message":"ok"}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.Message != "ok" {
		t.Fatalf("input = %#v", input)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"message":"ok","extra":true}`),
		json.RawMessage(`{"message":"ok"}{"message":"again"}`),
		json.RawMessage(`[]`),
	} {
		if err := DecodeArguments(raw, &input); err == nil {
			t.Fatalf("accepted invalid arguments: %s", raw)
		}
	}
}
