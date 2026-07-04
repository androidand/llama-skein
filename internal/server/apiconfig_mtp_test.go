package server

import (
	"reflect"
	"testing"

	"github.com/androidand/llama-skein/pkg/apicontract"
)

func TestParseMTPFlags(t *testing.T) {
	tests := []struct {
		name     string
		parts    []string
		expected *apicontract.MtpMetadata
	}{
		{
			name: "no MTP flags",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--ctx-size", "4096",
			},
			expected: nil,
		},
		{
			name: "MTP enabled with spec-type",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--spec-type", "draft-mtp",
				"--ctx-size", "4096",
			},
			expected: &apicontract.MtpMetadata{
				Enabled:  ptrOf(true),
				SpecType: ptrOf(apicontract.DraftMtp),
				Source:   ptrOf(apicontract.Cmd),
			},
		},
		{
			name: "MTP enabled with spec-draft-n-max",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--spec-type", "draft-mtp",
				"--spec-draft-n-max", "4",
				"--ctx-size", "4096",
			},
			expected: &apicontract.MtpMetadata{
				Enabled:   ptrOf(true),
				SpecType:  ptrOf(apicontract.DraftMtp),
				DraftNMax: ptrOf(4),
				Source:    ptrOf(apicontract.Cmd),
			},
		},
		{
			name: "MTP enabled with model-draft",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--spec-type", "draft-mtp",
				"--model-draft", "/path/to/draft.gguf",
				"--ctx-size", "4096",
			},
			expected: &apicontract.MtpMetadata{
				Enabled:    ptrOf(true),
				SpecType:   ptrOf(apicontract.DraftMtp),
				ModelDraft: ptrOf("/path/to/draft.gguf"),
				Source:     ptrOf(apicontract.Cmd),
			},
		},
		{
			name: "MTP enabled with all flags",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--spec-type", "draft-mtp",
				"--spec-draft-n-max", "3",
				"--model-draft", "/path/to/draft.gguf",
				"--ctx-size", "4096",
			},
			expected: &apicontract.MtpMetadata{
				Enabled:    ptrOf(true),
				SpecType:   ptrOf(apicontract.DraftMtp),
				DraftNMax:  ptrOf(3),
				ModelDraft: ptrOf("/path/to/draft.gguf"),
				Source:     ptrOf(apicontract.Cmd),
			},
		},
		{
			name: "MTP disabled (spec-type not draft-mtp)",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--spec-type", "some-other-type",
				"--ctx-size", "4096",
			},
			expected: nil,
		},
		{
			name: "MTP disabled (no spec-type)",
			parts: []string{
				"llama-server",
				"-m", "/path/to/model.gguf",
				"--ctx-size", "4096",
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMTPFlags(tt.parts)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if !reflect.DeepEqual(result.Mtp, tt.expected) {
				t.Errorf("mtp metadata mismatch:\nexpected: %+v\ngot: %+v", tt.expected, result.Mtp)
			}
		})
	}
}
