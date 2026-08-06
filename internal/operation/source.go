package operation

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrUntrustedSource is returned by ResolveArtifactURL when a plan's
// source_repository, source_revision, or an artifact's path fails safe
// composition (design.md decision 7: "repository, revision, and paths are
// encoded independently, never composed from an unchecked full path").
var ErrUntrustedSource = errors.New("operation: untrusted source")

// huggingFaceEndpoint is the only currently-allowed source host. design.md
// decision 7 also names "explicit loopback test sources" as allowed; not
// added here because no config plumbing exists yet to declare one safely —
// hardcoding a second always-on host would be a bigger hole than the one
// this function closes.
const huggingFaceEndpoint = "https://huggingface.co"

// repositoryRe matches "org/repo": exactly one slash, each side starting with
// an alphanumeric and continuing with alphanumerics, dots, hyphens or
// underscores. Deliberately stricter than Hugging Face's actual namespace
// rules — the goal is rejecting anything that isn't obviously a bare
// repository ID (a scheme, a host, an extra path segment, "..", a leading
// slash), not accepting every legal repository name.
//
// Note that only the FIRST character of each side is constrained: "org/repo."
// and "org/repo-" are accepted. That is safe here — a bare "." or ".."
// *segment* is rejected by safePathSegments, and ResolveArtifactDestination
// re-checks containment — but it is narrower than it looks, so don't read this
// as a general-purpose name validator. Pinned by
// TestHostBoundaryGoldenPinsKnownGaps.
var repositoryRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// revisionRe matches a short or full commit SHA. design.md decision 2
// requires an immutable revision, not a mutable branch name; a hex-only
// pattern is both a reasonable SHA shape check and incidentally excludes
// branch names entirely (they are almost never pure hex).
var revisionRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// ResolveArtifactURL safely composes the download URL for one artifact from
// already-validated-shape plan fields, porting the safe-composition pattern
// from Skein's tested Hugging Face URL normalization
// (internal/providers/download_url.go, commit b687c70c1374d42b3a6f6c486057330d0542bf9d) —
// building the URL from independently-checked parts via net/url, never by
// trusting or concatenating a caller-supplied path or URL string.
//
// Skein's version parses a free-form pasted URL (file-info/blob/resolve
// shapes) into repository/revision/filename; that parsing isn't needed here
// because ModelInstallPlan already carries those as separate structured
// fields (opencode-skein is where a pasted URL gets resolved into that shape
// — design.md's "opencode-skein owns discovery... llama-skein owns all
// host-local facts and mutations"). What transfers is the discipline: even
// though the fields arrive pre-split, this still validates each one's shape
// before composing, rather than trusting that a client which already sent a
// well-formed plan will keep doing so.
func ResolveArtifactURL(repository, revision, artifactPath string) (string, error) {
	if !repositoryRe.MatchString(repository) {
		return "", fmt.Errorf("%w: source_repository %q is not a bare org/repo id", ErrUntrustedSource, repository)
	}
	if !revisionRe.MatchString(revision) {
		return "", fmt.Errorf("%w: source_revision %q is not a commit SHA", ErrUntrustedSource, revision)
	}
	segments, err := safePathSegments(artifactPath)
	if err != nil {
		return "", fmt.Errorf("%w: artifact path %q: %v", ErrUntrustedSource, artifactPath, err)
	}

	base, err := url.Parse(huggingFaceEndpoint)
	if err != nil {
		return "", fmt.Errorf("operation: internal: %w", err) // huggingFaceEndpoint is a constant; only reachable if that constant is ever broken.
	}
	repoParts := strings.SplitN(repository, "/", 2)
	pathSegments := append([]string{"", repoParts[0], repoParts[1], "resolve", revision}, segments...)
	base.Path = strings.Join(pathSegments, "/")
	return base.String(), nil
}

// ResolveArtifactDestination safely composes the on-disk destination for one
// artifact under modelsDir, the other half of design.md decision 7's
// "destination paths are resolved under the configured models directory."
// Structurally the same discipline as ResolveArtifactURL (independently
// validate repository/path shape, never trust a composed string) plus one
// more check ResolveArtifactURL doesn't need: after joining, the resolved
// absolute path must still be *inside* modelsDir. filepath.Join alone
// cannot guarantee that on its own — safePathSegments already forbids the
// obvious ".."-segment escape, but this is defense in depth against
// modelsDir itself being misconfigured (e.g. a trailing-slash mismatch) or
// a future caller reusing the join logic without first calling
// safePathSegments.
func ResolveArtifactDestination(modelsDir, repository, artifactPath string) (string, error) {
	if !repositoryRe.MatchString(repository) {
		return "", fmt.Errorf("%w: source_repository %q is not a bare org/repo id", ErrUntrustedSource, repository)
	}
	segments, err := safePathSegments(artifactPath)
	if err != nil {
		return "", fmt.Errorf("%w: artifact path %q: %v", ErrUntrustedSource, artifactPath, err)
	}

	absModelsDir, err := filepath.Abs(modelsDir)
	if err != nil {
		return "", fmt.Errorf("operation: models directory %q: %w", modelsDir, err)
	}
	repoParts := strings.SplitN(repository, "/", 2)
	dest := filepath.Join(append([]string{absModelsDir, repoParts[0], repoParts[1]}, segments...)...)

	// filepath.Join already cleans the result, but the containment check
	// still needs an exact-directory prefix match (absModelsDir+separator),
	// not a plain string prefix — "/models-archive" must not pass a
	// containment check against "/models".
	if dest != absModelsDir && !strings.HasPrefix(dest, absModelsDir+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: resolved destination %q escapes the models directory", ErrUntrustedSource, dest)
	}
	return dest, nil
}

// safePathSegments splits and validates an artifact path: no leading slash,
// no empty segments (rules out "//" and a trailing slash), and no "."/".."
// segment (rules out traversal and no-op segments alike — an artifact path
// names one file, a "." segment can only be noise or an attempt to confuse
// a naive joiner).
func safePathSegments(p string) ([]string, error) {
	if p == "" {
		return nil, errors.New("path is empty")
	}
	if strings.HasPrefix(p, "/") {
		return nil, errors.New("path must not be absolute")
	}
	segments := strings.Split(p, "/")
	for _, segment := range segments {
		switch segment {
		case "":
			return nil, errors.New("path has an empty segment")
		case ".", "..":
			return nil, fmt.Errorf("path contains a %q segment", segment)
		}
	}
	return segments, nil
}
