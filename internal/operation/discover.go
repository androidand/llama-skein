package operation

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// hfApiEndpoint is the default HuggingFace API base. HFApiClient.BaseURL
// overrides it (tests point it at a local httptest server).
const hfApiEndpoint = "https://huggingface.co"

// HFApiClient queries the HuggingFace API for repository metadata.
type HFApiClient struct {
	HTTPClient *http.Client
	Token      string
	BaseURL    string // API base, defaults to hfApiEndpoint.
}

// baseURL returns the effective API base URL.
func (c *HFApiClient) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return hfApiEndpoint
}

// HFFileEntry represents a file in a HuggingFace repository. The HF model
// tree API (GET /api/models/{repo}/tree/{revision}) returns a JSON array of
// these, one per repository entry.
type HFFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Type string `json:"type"` // "file" or "directory"
}

// CompanionPattern defines how to detect companion GGUF files by filename.
type CompanionPattern struct {
	Prefix string
	Role   ArtifactRole
}

// KnownCompanionPatterns are the recognized companion GGUF filename patterns.
var KnownCompanionPatterns = []CompanionPattern{
	{Prefix: "mmproj-", Role: ArtifactRoleProjector},
	{Prefix: "dflash-", Role: ArtifactRoleDraft},
	{Prefix: "mtp-", Role: ArtifactRoleDraft},
	{Prefix: "eagle3-", Role: ArtifactRoleDraft},
}

// DiscoverCompanions queries the HuggingFace API to discover GGUF files in a
// repository, returning the main weights artifact and any companion artifacts
// (mmproj, dflash, mtp, eagle3) found.
//
// weightFilter restricts the main weights selection: when non-empty, only the
// weight GGUF whose path contains weightFilter is returned with role Weights;
// any other GGUF weights that are not recognized companions are excluded from
// the result entirely. When weightFilter is empty and the repository has
// multiple weight variants, every non-companion GGUF is returned as Weights.
func (c *HFApiClient) DiscoverCompanions(repository, revision, weightFilter string) ([]Artifact, error) {
	entries, err := c.fetchModelInfo(repository, revision)
	if err != nil {
		return nil, fmt.Errorf("discover companions: %w", err)
	}

	var weights []Artifact
	var companions []Artifact

	for _, sibling := range entries {
		if sibling.Type != "file" {
			continue
		}
		if !isGGUFFile(sibling.Path) {
			continue
		}

		artifact := Artifact{
			Path:      sibling.Path,
			SizeBytes: sibling.Size,
			Role:      ArtifactRoleWeights,
		}

		// Check if this is a companion file
		if role := detectCompanionRole(sibling.Path); role != "" {
			artifact.Role = role
			companions = append(companions, artifact)
		} else {
			weights = append(weights, artifact)
		}
	}

	if weightFilter != "" {
		var matched []Artifact
		for _, w := range weights {
			if strings.Contains(w.Path, weightFilter) {
				matched = append(matched, w)
			}
		}
		if len(matched) == 0 {
			return nil, fmt.Errorf("no weight GGUF in %s matches weight_filter %q (found: %s)", repository, weightFilter, weightPaths(weights))
		}
		weights = matched

		// A filter means the caller wants one specific variant. Collapse each
		// companion role to a single artifact too: the variant whose path
		// matches the filter, else the smallest (deterministic default).
		companions = selectCompanionVariants(companions, weightFilter)
	}

	// Return weights first, then companions
	result := make([]Artifact, 0, len(weights)+len(companions))
	result = append(result, weights...)
	result = append(result, companions...)
	return result, nil
}

// selectCompanionVariants reduces companions to at most one artifact per role
// when a weight_filter is active: the candidate whose path contains filter,
// falling back to the smallest of each role group so the selection is
// deterministic. Artifacts of role Weights are passed through untouched.
func selectCompanionVariants(companions []Artifact, filter string) []Artifact {
	byRole := make(map[ArtifactRole][]Artifact)
	var order []ArtifactRole
	for _, a := range companions {
		if _, ok := byRole[a.Role]; !ok {
			order = append(order, a.Role)
		}
		byRole[a.Role] = append(byRole[a.Role], a)
	}

	var picked []Artifact
	for _, role := range order {
		group := byRole[role]
		if len(group) == 1 {
			picked = append(picked, group[0])
			continue
		}
		var best *Artifact
		var bestFilter *Artifact
		for i := range group {
			a := &group[i]
			if strings.Contains(a.Path, filter) {
				if bestFilter == nil {
					bestFilter = a
				}
				continue
			}
			if best == nil || a.SizeBytes < best.SizeBytes {
				best = a
			}
		}
		if bestFilter != nil {
			picked = append(picked, *bestFilter)
		} else {
			picked = append(picked, *best)
		}
	}
	return picked
}

// weightPaths joins the weight artifact paths for error messages.
func weightPaths(weights []Artifact) string {
	paths := make([]string, len(weights))
	for i, w := range weights {
		paths[i] = w.Path
	}
	return strings.Join(paths, ", ")
}

// fetchModelInfo calls the HuggingFace model tree API to get repository file
// listings. It returns the entries as a slice of HFFileEntry.
func (c *HFApiClient) fetchModelInfo(repository, revision string) ([]HFFileEntry, error) {
	url := fmt.Sprintf("%s/api/models/%s/tree/%s", c.baseURL(), repository, revision)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch model info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HF API returned %d: %s", resp.StatusCode, string(body))
	}

	var entries []HFFileEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return entries, nil
}

// isGGUFFile checks if a filename has a .gguf extension.
func isGGUFFile(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".gguf")
}

// detectCompanionRole returns the artifact role for a companion file, or "" if
// it's a main weights file.
func detectCompanionRole(path string) ArtifactRole {
	filename := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		filename = path[idx+1:]
	}
	filename = strings.ToLower(filename)

	for _, pattern := range KnownCompanionPatterns {
		if strings.HasPrefix(filename, pattern.Prefix) {
			return pattern.Role
		}
	}
	return ""
}

// FilterGGUFFiles returns only the GGUF files from a list of artifacts,
// categorizing them as weights or companions.
func FilterGGUFFiles(artifacts []Artifact) (weights, companions []Artifact) {
	for _, a := range artifacts {
		if !isGGUFFile(a.Path) {
			continue
		}
		if a.Role == ArtifactRoleWeights {
			weights = append(weights, a)
		} else {
			companions = append(companions, a)
		}
	}
	return
}
