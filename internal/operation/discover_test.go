package operation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverCompanions(t *testing.T) {
	// Mock HuggingFace tree API response: a JSON array of repo entries.
	repoFiles := []HFFileEntry{
		{Path: "model-Q8_0.gguf", Size: 29_600_000_000, Type: "file"},
		{Path: "model-Q4_K_M.gguf", Size: 15_900_000_000, Type: "file"},
		{Path: "dflash-kquant.gguf", Size: 1_630_000_000, Type: "file"},
		{Path: "mmproj-kquant.gguf", Size: 1_400_000_000, Type: "file"},
		{Path: "tokenizer.json", Size: 500_000, Type: "file"},
		{Path: "README.md", Size: 1_000, Type: "file"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tree/abc1234") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(repoFiles)
	}))
	defer server.Close()

	// With a weight filter, only the matching weight is selected.
	c := &HFApiClient{HTTPClient: server.Client(), BaseURL: server.URL}
	got, err := c.DiscoverCompanions("meta-models/Muse-Glimmer-30B-GGUF", "abc1234", "Q8_0")
	if err != nil {
		t.Fatalf("DiscoverCompanions: %v", err)
	}

	want := []Artifact{
		{Path: "model-Q8_0.gguf", SizeBytes: 29_600_000_000, Role: ArtifactRoleWeights},
		{Path: "dflash-kquant.gguf", SizeBytes: 1_630_000_000, Role: ArtifactRoleDraft},
		{Path: "mmproj-kquant.gguf", SizeBytes: 1_400_000_000, Role: ArtifactRoleProjector},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverCompanions(filter=Q8_0) = %+v, want %+v", got, want)
	}

	// Without a weight filter, every non-companion GGUF is a weight.
	gotAll, err := c.DiscoverCompanions("test-org/repo", "abc1234", "")
	if err != nil {
		t.Fatalf("DiscoverCompanions (no filter): %v", err)
	}
	if len(gotAll) != 4 {
		t.Errorf("DiscoverCompanions() (no filter) got %d artifacts, want 4 (2 weights + 2 companions)", len(gotAll))
	}

	// A filter matching nothing returns an error listing available quants.
	if _, err := c.DiscoverCompanions("test-org/repo", "abc1234", "BF16"); err == nil {
		t.Errorf("DiscoverCompanions(BF16) = nil error, want one")
	}

	// Test isGGUFFile
	tests := []struct {
		path string
		want bool
	}{
		{"model.gguf", true},
		{"model-Q8_0.gguf", true},
		{"model.Q8_0.GGUF", true},
		{"tokenizer.json", false},
		{"README.md", false},
		{"subdir/model.gguf", true},
	}
	for _, tt := range tests {
		if got := isGGUFFile(tt.path); got != tt.want {
			t.Errorf("isGGUFFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}

	// Test detectCompanionRole
	roleTests := []struct {
		path string
		want ArtifactRole
	}{
		{"mmproj-kquant.gguf", ArtifactRoleProjector},
		{"mmproj-f16.gguf", ArtifactRoleProjector},
		{"dflash-kquant.gguf", ArtifactRoleDraft},
		{"mtp-draft.gguf", ArtifactRoleDraft},
		{"eagle3-draft.gguf", ArtifactRoleDraft},
		{"model-Q8_0.gguf", ""},
		{"tokenizer.json", ""},
	}
	for _, tt := range roleTests {
		if got := detectCompanionRole(tt.path); got != tt.want {
			t.Errorf("detectCompanionRole(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestDiscoverCompanionsSelectsVariantByFilter(t *testing.T) {
	// unsloth-style repo: many weight quants and multiple projector variants.
	repoFiles := []HFFileEntry{
		{Path: "Muse-Glimmer-30B-Q8_0.gguf", Size: 29_600_000_000, Type: "file"},
		{Path: "Muse-Glimmer-30B-UD-Q5_K_M.gguf", Size: 19_190_000_000, Type: "file"},
		{Path: "dflash-kquant.gguf", Size: 1_630_000_000, Type: "file"},
		{Path: "mmproj-kquant.gguf", Size: 1_400_000_000, Type: "file"},
		{Path: "mmproj-Muse-Glimmer-30B-Q8_0.gguf", Size: 2_050_000_000, Type: "file"},
		{Path: "mmproj-Muse-Glimmer-30B-BF16.gguf", Size: 3_850_000_000, Type: "file"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(repoFiles)
	}))
	defer server.Close()

	c := &HFApiClient{HTTPClient: server.Client(), BaseURL: server.URL}

	// Filter "Q8_0": the weights pick is obvious, and the projector variant
	// whose path also contains Q8_0 wins over the other projectors.
	got, err := c.DiscoverCompanions("unsloth/Muse-Glimmer-30B-GGUF", "abc1234", "Q8_0")
	if err != nil {
		t.Fatalf("DiscoverCompanions: %v", err)
	}
	want := []Artifact{
		{Path: "Muse-Glimmer-30B-Q8_0.gguf", SizeBytes: 29_600_000_000, Role: ArtifactRoleWeights},
		{Path: "dflash-kquant.gguf", SizeBytes: 1_630_000_000, Role: ArtifactRoleDraft},
		{Path: "mmproj-Muse-Glimmer-30B-Q8_0.gguf", SizeBytes: 2_050_000_000, Role: ArtifactRoleProjector},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverCompanions(Q8_0) = %+v, want %+v", got, want)
	}

	// Filter that only matches weights (Q5_K_M): the projector falls back to
	// the smallest variant (mmproj-kquant) since none contains the filter.
	got2, err := c.DiscoverCompanions("unsloth/Muse-Glimmer-30B-GGUF", "abc1234", "Q5_K_M")
	if err != nil {
		t.Fatalf("DiscoverCompanions: %v", err)
	}
	want2 := []Artifact{
		{Path: "Muse-Glimmer-30B-UD-Q5_K_M.gguf", SizeBytes: 19_190_000_000, Role: ArtifactRoleWeights},
		{Path: "dflash-kquant.gguf", SizeBytes: 1_630_000_000, Role: ArtifactRoleDraft},
		{Path: "mmproj-kquant.gguf", SizeBytes: 1_400_000_000, Role: ArtifactRoleProjector},
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("DiscoverCompanions(Q5_K_M) = %+v, want %+v", got2, want2)
	}
}

func TestFilterGGUFFiles(t *testing.T) {
	artifacts := []Artifact{
		{Path: "model-Q8_0.gguf", SizeBytes: 29_600_000_000, Role: ArtifactRoleWeights},
		{Path: "dflash-kquant.gguf", SizeBytes: 1_630_000_000, Role: ArtifactRoleDraft},
		{Path: "mmproj-kquant.gguf", SizeBytes: 1_400_000_000, Role: ArtifactRoleProjector},
		{Path: "tokenizer.json", SizeBytes: 500_000, Role: ArtifactRoleTokenizer},
		{Path: "README.md", SizeBytes: 1_000, Role: ArtifactRoleOther},
	}

	weights, companions := FilterGGUFFiles(artifacts)

	if len(weights) != 1 {
		t.Errorf("expected 1 weights artifact, got %d", len(weights))
	}
	if len(companions) != 2 {
		t.Errorf("expected 2 companions artifacts, got %d", len(companions))
	}
	if weights[0].Path != "model-Q8_0.gguf" {
		t.Errorf("expected weights path model-Q8_0.gguf, got %s", weights[0].Path)
	}
}
