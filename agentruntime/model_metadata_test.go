package agentruntime

import (
	"errors"
	"testing"
)

func TestModelMetadataValidate(t *testing.T) {
	tests := []struct {
		name     string
		metadata ModelMetadata
		valid    bool
	}{
		{name: "known limits", metadata: ModelMetadata{ContextWindowTokens: 128_000, MaxOutputTokens: 16_000}, valid: true},
		{name: "unknown output limit", metadata: ModelMetadata{ContextWindowTokens: 128_000}, valid: true},
		{name: "missing context window", metadata: ModelMetadata{MaxOutputTokens: 1}},
		{name: "negative output limit", metadata: ModelMetadata{ContextWindowTokens: 1, MaxOutputTokens: -1}},
		{name: "output larger than context", metadata: ModelMetadata{ContextWindowTokens: 10, MaxOutputTokens: 11}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.metadata.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid {
				if !errors.Is(err, ErrInvalidModelMetadata) {
					t.Fatalf("Validate() error = %v, want ErrInvalidModelMetadata", err)
				}
			}
		})
	}
}

func TestModelMetadataHasOutputLimit(t *testing.T) {
	if (ModelMetadata{ContextWindowTokens: 100}).HasOutputLimit() {
		t.Fatal("unknown output limit reported as known")
	}
	if !(ModelMetadata{ContextWindowTokens: 100, MaxOutputTokens: 1}).HasOutputLimit() {
		t.Fatal("known output limit reported as unknown")
	}
}
