package operation

// Golden export of llama-skein's host-boundary reference and shard behaviour
// (skein:fleet-model-gallery task 3.1).
//
// The host boundary is the line opencode-skein hands work across: it resolves a
// pasted Hugging Face URL into a structured (repository, revision, artifact
// path) triple, and llama-skein decides whether that triple is safe to fetch
// and where it lands on disk. Both sides have to agree on what a valid
// reference is, or a model the catalog happily resolves fails at install with a
// 400 the user cannot act on.
//
// These fixtures are portable on purpose, for the same reason the Skein-side
// quant/family exports are: the two ends of this boundary are written in
// different languages, and a JSON table of input → expected output is the only
// artifact both can check themselves against. The tests below are what make the
// files trustworthy — they are regenerated from and verified against the live
// functions on every run, so they cannot drift into documentation of behaviour
// llama-skein no longer has.
//
// Regenerate after an intentional behaviour change:
//
//	SKEIN_UPDATE_FIXTURES=1 go test ./internal/operation/ -run HostBoundaryGolden

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const (
	referenceGoldenPath = "testdata/reference-cases.json"
	shardGoldenPath     = "testdata/shard-cases.json"
)

// referenceCase is one (repository, revision, artifact path) triple and what
// llama-skein composes from it — or the reason it refuses.
type referenceCase struct {
	// Note is why the case exists, so a reader of the JSON alone knows what it
	// is guarding.
	Note         string `json:"note"`
	Repository   string `json:"repository"`
	Revision     string `json:"revision"`
	ArtifactPath string `json:"artifact_path"`
	// URL is the composed download URL, empty when the triple is rejected.
	URL string `json:"url"`
	// Rejected is true when ResolveArtifactURL refused the triple. The reason
	// text is deliberately not pinned — it is a human-facing message, and
	// pinning it would make every wording improvement a fixture break.
	Rejected bool `json:"rejected"`
	// UntrustedSource records whether the refusal came through
	// ErrUntrustedSource, which is the part callers actually branch on.
	UntrustedSource bool `json:"untrusted_source"`
}

// A valid-shaped revision, reused so cases vary in exactly one dimension.
const goldenRevision = "0123456789abcdef0123456789abcdef01234567"

var referenceCaseInputs = []struct{ note, repository, revision, artifactPath string }{
	// The ordinary path.
	{"canonical case: org/repo, full SHA, one GGUF", "unsloth/Qwen3-32B-GGUF", goldenRevision, "Qwen3-32B-Q4_K_M.gguf"},
	{"seven-character short SHA is the minimum accepted revision", "unsloth/Qwen3-32B-GGUF", "0123456", "model.gguf"},
	{"nested artifact path, as quantizer repos publish per-quant subdirectories", "unsloth/Qwen3-32B-GGUF", goldenRevision, "Q4_K_M/Qwen3-32B-Q4_K_M-00001-of-00002.gguf"},
	{"characters needing percent-encoding are encoded by net/url, not concatenated", "unsloth/Qwen3-32B-GGUF", goldenRevision, "model file (v2).gguf"},
	{"dots, hyphens and underscores are legal inside a name", "My-Org_1.0/repo.name_v2", goldenRevision, "model.gguf"},

	// Repository shape. This is the half opencode-skein must agree with.
	{"a full URL is not a bare repository id", "https://huggingface.co/unsloth/Qwen3-32B-GGUF", goldenRevision, "model.gguf"},
	{"owner with no repo", "just-a-repo-no-owner", goldenRevision, "model.gguf"},
	{"an extra path segment is not part of a repository id", "org/repo/extra", goldenRevision, "model.gguf"},
	{"empty repository", "", goldenRevision, "model.gguf"},
	{"traversal in the owner position", "../escape/repo", goldenRevision, "model.gguf"},
	{"an empty path segment between owner and repo", "org//repo", goldenRevision, "model.gguf"},
	{"BOUNDARY/PINNED: a trailing dot is ACCEPTED, though repositoryRe's comment claims names must end alphanumeric", "org/repo.", goldenRevision, "model.gguf"},
	{"BOUNDARY: a leading underscore is rejected — names must start alphanumeric", "org/_repo", goldenRevision, "model.gguf"},
	{"BOUNDARY/PINNED: a trailing hyphen is ACCEPTED, same comment/pattern mismatch", "org/repo-", goldenRevision, "model.gguf"},
	{"BOUNDARY: non-ASCII is rejected even though Hugging Face permits it", "örg/repo", goldenRevision, "model.gguf"},
	{"BOUNDARY: a space is rejected", "org/re po", goldenRevision, "model.gguf"},
	{"single-character owner and repo are accepted", "a/b", goldenRevision, "model.gguf"},

	// Revision shape: immutable SHA only, by construction.
	{"a branch name is not an immutable revision", "org/repo", "main", "model.gguf"},
	{"a tag is not an immutable revision", "org/repo", "v1.0", "model.gguf"},
	{"fewer than seven hex characters", "org/repo", "dead", "model.gguf"},
	{"non-hex characters", "org/repo", "nothex123", "model.gguf"},
	{"empty revision", "org/repo", "", "model.gguf"},
	{"uppercase hex is accepted", "org/repo", "ABCDEF0123456789ABCDEF0123456789ABCDEF01", "model.gguf"},

	// Artifact path shape: traversal and noise segments.
	{"empty artifact path", "org/repo", goldenRevision, ""},
	{"absolute artifact path", "org/repo", goldenRevision, "/etc/passwd"},
	{"classic traversal", "org/repo", goldenRevision, "../../../etc/passwd"},
	{"traversal hidden mid-path", "org/repo", goldenRevision, "subdir/../secret.gguf"},
	{"a \".\" segment is noise an artifact path should never carry", "org/repo", goldenRevision, "./model.gguf"},
	{"a doubled slash leaves an empty segment", "org/repo", goldenRevision, "subdir//model.gguf"},
	{"a trailing slash leaves an empty segment", "org/repo", goldenRevision, "subdir/"},
	{"a backslash is not a separator here, so it stays inside one segment", "org/repo", goldenRevision, `subdir\model.gguf`},
}

func TestHostBoundaryGoldenReferenceMatchesLiveBehaviour(t *testing.T) {
	observed := make([]referenceCase, 0, len(referenceCaseInputs))
	for _, in := range referenceCaseInputs {
		url, err := ResolveArtifactURL(in.repository, in.revision, in.artifactPath)
		observed = append(observed, referenceCase{
			Note:            in.note,
			Repository:      in.repository,
			Revision:        in.revision,
			ArtifactPath:    in.artifactPath,
			URL:             url,
			Rejected:        err != nil,
			UntrustedSource: errors.Is(err, ErrUntrustedSource),
		})
	}

	if os.Getenv("SKEIN_UPDATE_FIXTURES") == "1" {
		writeGolden(t, referenceGoldenPath, observed)
		t.Log("regenerated", referenceGoldenPath)
		return
	}

	var expected []referenceCase
	readGolden(t, referenceGoldenPath, &expected)
	if len(expected) != len(observed) {
		t.Fatalf("fixture has %d cases, code produces %d — regenerate with SKEIN_UPDATE_FIXTURES=1",
			len(expected), len(observed))
	}
	for i, want := range expected {
		got := observed[i]
		if got.Repository != want.Repository || got.Revision != want.Revision || got.ArtifactPath != want.ArtifactPath {
			t.Errorf("case %d: fixture is for (%q,%q,%q) but the case list has (%q,%q,%q)",
				i, want.Repository, want.Revision, want.ArtifactPath,
				got.Repository, got.Revision, got.ArtifactPath)
			continue
		}
		if got != want {
			t.Errorf("%s\n  (%q, %q, %q)\n  fixture: url=%q rejected=%v untrusted=%v\n  code:    url=%q rejected=%v untrusted=%v",
				want.Note, want.Repository, want.Revision, want.ArtifactPath,
				want.URL, want.Rejected, want.UntrustedSource,
				got.URL, got.Rejected, got.UntrustedSource)
		}
	}
}

// shardCase is one set of filenames as published by a repository, and how
// llama-skein groups it and judges completeness.
type shardCase struct {
	Note      string       `json:"note"`
	Filenames []string     `json:"filenames"`
	Groups    []shardGroup `json:"groups"`
}

// shardGroup is one grouped shard set. Emitted as a sorted slice rather than a
// map so the fixture is byte-stable across runs.
type shardGroup struct {
	Key     string   `json:"key"`
	Members []string `json:"members"`
	// Complete and Total are ShardSetComplete's verdict for this group.
	Complete bool   `json:"complete"`
	Total    uint32 `json:"total"`
}

var shardCaseInputs = []struct {
	note      string
	filenames []string
}{
	{"a complete two-part set", []string{
		"Qwen3-235B-Q4_K_M-00001-of-00002.gguf",
		"Qwen3-235B-Q4_K_M-00002-of-00002.gguf",
	}},
	{"shards listed out of order are grouped in index order", []string{
		"model-00003-of-00003.gguf",
		"model-00001-of-00003.gguf",
		"model-00002-of-00003.gguf",
	}},
	{"a gap in the middle is incomplete, but the declared total survives", []string{
		"model-00001-of-00003.gguf",
		"model-00003-of-00003.gguf",
	}},
	{"two quant sets at the same total stay separate", []string{
		"Q4_K_M/model-00001-of-00002.gguf",
		"Q4_K_M/model-00002-of-00002.gguf",
		"Q5_K_M/model-00001-of-00002.gguf",
		"Q5_K_M/model-00002-of-00002.gguf",
	}},
	{"an unsharded file is its own singleton group", []string{"Qwen3-32B-Q4_K_M.gguf"}},
	{"weights plus auxiliaries: only the weights form a shard set", []string{
		"model-00001-of-00002.gguf",
		"model-00002-of-00002.gguf",
		"mmproj-model-f16.gguf",
	}},
	{"a duplicate index is not a complete set even at the right count", []string{
		"model-00001-of-00002.gguf",
		"model-00001-of-00002.gguf",
	}},
	{"disagreeing totals do not form a complete set", []string{
		"model-00001-of-00002.gguf",
		"model-00002-of-00003.gguf",
	}},
	{"PINNED: an out-of-range index is not an error — it degrades to a singleton", []string{
		"model-00004-of-00003.gguf",
	}},
	{"PINNED: index zero degrades to a singleton the same way", []string{
		"model-00000-of-00003.gguf",
	}},
	{"a non-GGUF extension is never a shard", []string{"model-00001-of-00002.bin"}},
	{"a quant suffix is not a shard suffix", []string{"model-Q4_K_M.gguf"}},
}

func TestHostBoundaryGoldenShardMatchesLiveBehaviour(t *testing.T) {
	observed := make([]shardCase, 0, len(shardCaseInputs))
	for _, in := range shardCaseInputs {
		grouped := GroupShards(in.filenames)
		keys := make([]string, 0, len(grouped))
		for key := range grouped {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		groups := make([]shardGroup, 0, len(keys))
		for _, key := range keys {
			complete, total := ShardSetComplete(grouped[key])
			groups = append(groups, shardGroup{
				Key:      key,
				Members:  grouped[key],
				Complete: complete,
				Total:    total,
			})
		}
		observed = append(observed, shardCase{Note: in.note, Filenames: in.filenames, Groups: groups})
	}

	if os.Getenv("SKEIN_UPDATE_FIXTURES") == "1" {
		writeGolden(t, shardGoldenPath, observed)
		t.Log("regenerated", shardGoldenPath)
		return
	}

	var expected []shardCase
	readGolden(t, shardGoldenPath, &expected)
	if len(expected) != len(observed) {
		t.Fatalf("fixture has %d cases, code produces %d — regenerate with SKEIN_UPDATE_FIXTURES=1",
			len(expected), len(observed))
	}
	for i, want := range expected {
		got := observed[i]
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Errorf("%s:\n  fixture: %s\n  code:    %s", want.Note, wantJSON, gotJSON)
		}
	}
}

// TestHostBoundaryGoldenPinsKnownGaps states the boundary behaviours the export
// carries that a reader would not predict, so neither end of the boundary can
// adopt them by accident. If one of these starts failing, llama-skein has
// changed and the fixture should be regenerated.
func TestHostBoundaryGoldenPinsKnownGaps(t *testing.T) {
	// A malformed shard marker is not an error. ParseShardInfo returns ok=false
	// and the file falls through to being treated as an ordinary single-file
	// model — so a repository that publishes "-00004-of-00003" installs one
	// truncated file rather than failing loudly.
	for _, name := range []string{"model-00004-of-00003.gguf", "model-00000-of-00003.gguf"} {
		if _, ok := ParseShardInfo(name); ok {
			t.Errorf("%s now parses as a shard; regenerate the fixture", name)
		}
		groups := GroupShards([]string{name})
		if len(groups) != 1 {
			t.Errorf("%s no longer degrades to a single group (got %d); regenerate the fixture", name, len(groups))
		}
	}

	// Shard completeness is judged per group, and a group is keyed by prefix
	// AND declared total. Two shards that each claim a different total are
	// therefore two separate singleton groups, not one incomplete set — the
	// mismatch is invisible at this layer.
	groups := GroupShards([]string{"model-00001-of-00002.gguf", "model-00002-of-00003.gguf"})
	if len(groups) != 2 {
		t.Errorf("disagreeing totals now form %d group(s), not 2; regenerate the fixture", len(groups))
	}

	// The repository rule is stricter than Hugging Face's own in one direction:
	// a name must START alphanumeric and stay ASCII. opencode-skein must reject
	// these at the catalog end too, or the user gets a 400 at install time for
	// a model the gallery showed as available.
	for _, repo := range []string{"org/_repo", "örg/repo", "org/re po"} {
		if _, err := ResolveArtifactURL(repo, goldenRevision, "model.gguf"); err == nil {
			t.Errorf("%q is now accepted; the boundary widened — regenerate the fixture and tell opencode-skein", repo)
		}
	}

	// PINNED, and the reason this test exists: repositoryRe's doc comment says
	// each side must start "and end" with an alphanumeric, but the pattern
	// `[A-Za-z0-9][A-Za-z0-9._-]*` only anchors the start — a trailing "." or
	// "-" is accepted. Not a traversal risk (a bare ".."/"." segment is
	// rejected separately, and ResolveArtifactDestination re-checks
	// containment), but it means an auditor reading the comment would believe
	// a check exists that does not. The comment has been corrected to describe
	// the pattern; tightening the pattern to match the original comment is a
	// behaviour change owned by host-model-management-api, not something to
	// slip in under a fixture export, so it is pinned here instead.
	for _, repo := range []string{"org/repo.", "org/repo-"} {
		if _, err := ResolveArtifactURL(repo, goldenRevision, "model.gguf"); err != nil {
			t.Errorf("%q is now rejected — the trailing-character gap was closed; regenerate the fixture", repo)
		}
	}

	// Only an immutable SHA is a revision. "main" is the single most likely
	// thing a caller composing a URL by hand would send.
	if _, err := ResolveArtifactURL("org/repo", "main", "model.gguf"); err == nil {
		t.Error(`revision "main" is now accepted; mutable refs were supposed to be structurally impossible`)
	}
}

func readGolden(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden export: %v (regenerate with SKEIN_UPDATE_FIXTURES=1)", err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decode golden export %s: %v", path, err)
	}
}

func writeGolden(t *testing.T, path string, cases any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// encoding/json escapes '<', '>' and '&' by default (HTML-safety). These
	// fixtures are read by humans and by a TypeScript port, neither of which
	// wants "\u003e" in place of ">".
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cases); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
