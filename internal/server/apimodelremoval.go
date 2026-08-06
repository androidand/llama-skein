package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/androidand/llama-skein/internal/operation"
)

// pathIsContained reports whether path is inside baseDir. Exact-directory
// prefix match (absBase+separator), not a plain string prefix — the same
// discipline internal/operation.ResolveArtifactDestination already
// established (task 3.3's fix for the "models" vs "models-archive"
// false-positive a naive strings.HasPrefix(dest, modelsDir) would have),
// applied here to a path this code did not compose but must still validate
// before deleting it.
func pathIsContained(path, baseDir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return false
	}
	return absPath == absBase || strings.HasPrefix(absPath, absBase+string(filepath.Separator))
}

// resolveArtifactSetForRemoval returns every on-disk path design.md
// decision 6 means by "the complete installed artifact set" for a model
// whose primary weights file is primaryPath (task 5.3: "validate artifact
// ownership... remove the complete installed artifact set"). Every path in
// the returned set is validated to be contained within s.modelsDir() — the
// whole call fails closed (returns an error, resolves nothing) if
// modelsDir is unknown or if any candidate path fails containment, rather
// than silently deleting a partial or unsafe set.
//
// Two sources, tried in order:
//  1. The most recent succeeded install operation for this model_id (task
//     5.1's provenance, opIdx.succeeded): every artifact path it
//     submitted, resolved under its own source_repository via the same
//     operation.ResolveArtifactDestination task 3.3 uses to accept a
//     plan — this is the accurate, complete set for anything installed
//     through the operation API (sections 3-4), including shards and
//     auxiliaries the primary file's own directory listing alone can't
//     distinguish from unrelated files.
//  2. A shard-sibling scan of primaryPath's own directory
//     (operation.GroupShards, task 3.2/4.5's shard convention) — the
//     fallback for a model configured by hand or pulled through the older
//     POST /api/models/pull route, which has no operation provenance at
//     all. A non-sharded primaryPath's "set" here is just itself.
func (s *Server) resolveArtifactSetForRemoval(id, primaryPath string, opIdx modelOperationIndex) ([]string, error) {
	modelsDir := s.modelsDir()
	if modelsDir == "" {
		return nil, errors.New("models directory unknown; cannot safely validate artifact ownership")
	}
	if !pathIsContained(primaryPath, modelsDir) {
		return nil, fmt.Errorf("model file %q is outside the configured models directory %q; refusing to delete", primaryPath, modelsDir)
	}

	if op, ok := opIdx.succeeded[id]; ok {
		seen := map[string]bool{primaryPath: true}
		paths := []string{primaryPath}
		for _, a := range op.Artifacts {
			dest, err := operation.ResolveArtifactDestination(modelsDir, op.SourceRepository, a.Path)
			if err != nil {
				// The plan or modelsDir changed shape since this operation
				// succeeded; skip a path that no longer resolves safely
				// rather than fail the whole removal on stale provenance.
				continue
			}
			if !seen[dest] {
				seen[dest] = true
				paths = append(paths, dest)
			}
		}
		sort.Strings(paths)
		return paths, nil
	}

	return s.shardSiblingsOf(primaryPath), nil
}

// shardSiblingsOf scans primaryPath's own directory for files sharing its
// shard group (operation.GroupShards). Returns just primaryPath, still a
// safe and valid answer, when the directory can't be read or primaryPath
// doesn't look like part of a shard set.
func (s *Server) shardSiblingsOf(primaryPath string) []string {
	dir := filepath.Dir(primaryPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{primaryPath}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	primaryName := filepath.Base(primaryPath)
	for _, group := range operation.GroupShards(names) {
		inGroup := false
		for _, name := range group {
			if name == primaryName {
				inGroup = true
				break
			}
		}
		if !inGroup {
			continue
		}
		paths := make([]string, len(group))
		for i, name := range group {
			paths[i] = filepath.Join(dir, name)
		}
		sort.Strings(paths)
		return paths
	}
	return []string{primaryPath}
}
