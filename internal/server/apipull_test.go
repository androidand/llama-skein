package server

// Host-boundary tests for the legacy free-form pull reference parser
// (skein:fleet-model-gallery task 3.1).
//
// Why this file exists: resolveHFSource and isHuggingFaceHost are duplicated
// byte-for-byte (modulo two comments) between this package and
// proxy/proxymanager_models.go. The proxy copy has six table cases in
// proxy/proxymanager_models_test.go. This copy — the one behind the live
// POST /api/models/pull route — had none. A security-relevant allowlist with a
// tested twin and an untested original is worse than an untested function
// alone, because the tests create the impression the behaviour is covered.
//
// This path is scheduled for deletion by host-model-management-api task 6.4,
// which replaces free-form references with the structured ModelInstallPlan
// resolved in internal/operation/source.go. It is still the live route until
// then, so it is pinned rather than left bare: the cases below record what it
// does today, including the parts that are wrong, so 6.4 can delete it knowing
// exactly what is being dropped.

import (
	"os"
	"strings"
	"testing"
)

func TestIsHuggingFaceHost(t *testing.T) {
	cases := map[string]bool{
		"huggingface.co":                  true,
		"HuggingFace.CO":                  true, // lowercased before comparison
		"cdn-lfs.huggingface.co":          true,
		"huggingface.co.evil.example.com": false, // suffix match is on ".huggingface.co", not a substring
		"nothuggingface.co":               false, // no leading dot, so not a subdomain
		"example.com":                     false,
		"":                                false,
	}
	for host, want := range cases {
		t.Run(host, func(t *testing.T) {
			if got := isHuggingFaceHost(host); got != want {
				t.Fatalf("isHuggingFaceHost(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestResolveHFSource(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		wantURL      string
		wantFilename string
		wantErr      string // substring; empty means success expected
	}{
		{
			name:         "short owner/repo/file form",
			model:        "unsloth/Qwen3-32B-GGUF/Qwen3-32B-Q4_K_M.gguf",
			wantURL:      "https://huggingface.co/unsloth/Qwen3-32B-GGUF/resolve/main/Qwen3-32B-Q4_K_M.gguf",
			wantFilename: "Qwen3-32B-Q4_K_M.gguf",
		},
		{
			name:         "full HuggingFace resolve URL passes through unchanged",
			model:        "https://huggingface.co/unsloth/Qwen3-32B-GGUF/resolve/main/Qwen3-32B-Q4_K_M.gguf",
			wantURL:      "https://huggingface.co/unsloth/Qwen3-32B-GGUF/resolve/main/Qwen3-32B-Q4_K_M.gguf",
			wantFilename: "Qwen3-32B-Q4_K_M.gguf",
		},
		{
			name:         "a CDN subdomain is allowed",
			model:        "https://cdn-lfs.huggingface.co/repo/model.gguf",
			wantURL:      "https://cdn-lfs.huggingface.co/repo/model.gguf",
			wantFilename: "model.gguf",
		},
		{
			name: "query string is stripped for the filename but kept in the URL",
			// The download URL must keep ?download=true (it is often what makes
			// the origin serve the file); only filename derivation strips it.
			model:        "https://huggingface.co/org/repo/resolve/main/model.gguf?download=true",
			wantURL:      "https://huggingface.co/org/repo/resolve/main/model.gguf?download=true",
			wantFilename: "model.gguf",
		},
		{
			name:         "loopback bypasses both the HTTPS requirement and the host allowlist",
			model:        "http://localhost:8080/model.gguf",
			wantURL:      "http://localhost:8080/model.gguf",
			wantFilename: "model.gguf",
		},
		{
			name:         "127.0.0.1 is loopback",
			model:        "http://127.0.0.1:9000/model.gguf",
			wantURL:      "http://127.0.0.1:9000/model.gguf",
			wantFilename: "model.gguf",
		},
		{
			name:         "IPv6 loopback is recognised (Hostname() strips the brackets)",
			model:        "http://[::1]:9000/model.gguf",
			wantURL:      "http://[::1]:9000/model.gguf",
			wantFilename: "model.gguf",
		},
		{
			name:    "a non-HuggingFace remote host is refused",
			model:   "https://evil.example.com/unsloth/model.gguf",
			wantErr: "is not allowed",
		},
		{
			name:    "plain HTTP to a remote host is refused before the host check",
			model:   "http://huggingface.co/org/repo/resolve/main/model.gguf",
			wantErr: "only HTTPS URLs are supported",
		},
		{
			name:    "two segments is not enough for the short form",
			model:   "unsloth/Qwen3-32B-GGUF",
			wantErr: "must be 'owner/repo/filename.gguf'",
		},
		{
			name:    "a bare word is not a reference",
			model:   "bad-format",
			wantErr: "must be 'owner/repo/filename.gguf'",
		},
		{
			// PINNED DEFECT: filepath.Base is applied to the whole URL string
			// and strips the trailing slash, so a directory URL silently
			// yields the *repository name* as the filename. The request then
			// downloads whatever that URL serves (an HTML page) into a file
			// called "repo". The "cannot derive filename" guard below it is
			// effectively unreachable for any http(s) input: Base only returns
			// "." or "" for an empty or "." string, and the input here always
			// starts with a scheme.
			name:         "PINNED: a directory URL yields the repo name as the filename",
			model:        "https://huggingface.co/org/repo/",
			wantURL:      "https://huggingface.co/org/repo/",
			wantFilename: "repo",
		},
		{
			// PINNED DEFECT: SplitN(_, "/", 3) leaves everything after the
			// second slash in `file`, which is then interpolated into the URL
			// with no escaping at all — unlike the structured path in
			// internal/operation/source.go, which composes through net/url.
			// Here it happens to produce the right URL for a nested file, but
			// the mechanism is string concatenation of caller input.
			name:         "PINNED: a nested path is interpolated raw, not encoded",
			model:        "org/repo/Q4_K_M/model-00001-of-00002.gguf",
			wantURL:      "https://huggingface.co/org/repo/resolve/main/Q4_K_M/model-00001-of-00002.gguf",
			wantFilename: "model-00001-of-00002.gguf",
		},
		{
			// PINNED DEFECT: the short form hardcodes "main" — a mutable ref.
			// The same download tomorrow can return different bytes. The
			// structured path forbids this by requiring a commit SHA.
			name:         "PINNED: the short form pins nothing — revision is always \"main\"",
			model:        "org/repo/model.gguf",
			wantURL:      "https://huggingface.co/org/repo/resolve/main/model.gguf",
			wantFilename: "model.gguf",
		},
		{
			// PINNED DEFECT: no traversal check on the short form. filepath.Base
			// keeps the filename sane, so the *destination* is safe (the caller
			// re-checks containment), but the composed URL still walks out of
			// the intended repository namespace.
			name:         "PINNED: \"..\" in the short form is not rejected here",
			model:        "org/repo/../../other/model.gguf",
			wantURL:      "https://huggingface.co/org/repo/resolve/main/../../other/model.gguf",
			wantFilename: "model.gguf",
		},
		{
			// PINNED: a space is interpolated raw into the URL rather than
			// percent-encoded, which the structured resolver does handle.
			name:         "PINNED: a space in the filename is not encoded",
			model:        "org/repo/model file.gguf",
			wantURL:      "https://huggingface.co/org/repo/resolve/main/model file.gguf",
			wantFilename: "model file.gguf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotFilename, err := resolveHFSource(tt.model)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveHFSource(%q) = (%q, %q, nil), want error containing %q",
						tt.model, gotURL, gotFilename, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveHFSource(%q) error = %q, want it to contain %q", tt.model, err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveHFSource(%q) = error %v, want (%q, %q)", tt.model, err, tt.wantURL, tt.wantFilename)
			}
			if gotURL != tt.wantURL {
				t.Errorf("url  = %q, want %q", gotURL, tt.wantURL)
			}
			if gotFilename != tt.wantFilename {
				t.Errorf("file = %q, want %q", gotFilename, tt.wantFilename)
			}
		})
	}
}

// TestResolveHFSourceMatchesProxyCopy guards the duplication itself. Both
// copies are unexported in their own packages, so this compares source text
// rather than behaviour — which is the right granularity anyway: the risk is
// one copy being patched and the other forgotten, and that shows up as a text
// difference long before anyone writes a test that distinguishes them.
//
// Comments and blank lines are ignored (the two copies already differ by two
// comment lines today). Deleting this test is correct once
// host-model-management-api task 6.4 removes one of the copies.
func TestResolveHFSourceMatchesProxyCopy(t *testing.T) {
	const (
		serverFile = "apipull.go"
		proxyFile  = "../../proxy/proxymanager_models.go"
	)
	for _, fn := range []string{"isHuggingFaceHost", "resolveHFSource"} {
		t.Run(fn, func(t *testing.T) {
			mine := extractFunc(t, serverFile, fn)
			theirs := extractFunc(t, proxyFile, fn)
			if mine != theirs {
				t.Fatalf("the two copies of %s have diverged.\n%s:\n%s\n\n%s:\n%s\n\n"+
					"Patch both, or finish host-model-management-api 6.4 and delete one.",
					fn, serverFile, mine, proxyFile, theirs)
			}
		})
	}
}

// extractFunc returns the body of a top-level function with comments, blank
// lines and leading indentation removed, so two copies can be compared for
// meaningful difference rather than formatting.
func extractFunc(t *testing.T, path, name string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(src), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "func "+name+"(") {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("%s: no top-level func %s found — the copies have diverged structurally", path, name)
	}
	var out []string
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		out = append(out, trimmed)
		if line == "}" { // top-level closing brace is unindented
			break
		}
	}
	return strings.Join(out, "\n")
}
