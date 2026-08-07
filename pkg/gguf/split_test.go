package gguf

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSized(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The regression this exists for: DeepSeek-V4-Flash UD-IQ2_M is 3 shards
// where shard 1 is 5 MB of a 91 GB model. Sizing from the opened file makes
// a 91 GB model look like it fits on any card.
func TestInspectSplit_SumsEveryShard(t *testing.T) {
	dir := t.TempDir()
	name := "Model-0731-UD-IQ2_M"
	writeSized(t, filepath.Join(dir, name+"-00001-of-00003.gguf"), 100)
	writeSized(t, filepath.Join(dir, name+"-00002-of-00003.gguf"), 5000)
	writeSized(t, filepath.Join(dir, name+"-00003-of-00003.gguf"), 4000)

	info := InspectSplit(filepath.Join(dir, name+"-00001-of-00003.gguf"))
	if !info.IsSplit {
		t.Fatal("a -0000N-of-0000M file must be recognized as a split")
	}
	if info.Count != 3 || info.Index != 1 {
		t.Fatalf("index/count = %d/%d", info.Index, info.Count)
	}
	if info.TotalBytes != 9100 {
		t.Fatalf("total = %d, want the sum of all three shards (9100)", info.TotalBytes)
	}
	if len(info.Missing) != 0 {
		t.Fatalf("missing = %v", info.Missing)
	}

	// Inspecting any other shard must report the same total.
	if other := InspectSplit(filepath.Join(dir, name+"-00002-of-00003.gguf")); other.TotalBytes != 9100 {
		t.Fatalf("total from shard 2 = %d", other.TotalBytes)
	}
}

// A shard referenced by the naming scheme but absent is reported, because
// llama.cpp cannot load the set without it.
func TestInspectSplit_ReportsMissingShards(t *testing.T) {
	dir := t.TempDir()
	name := "Model"
	writeSized(t, filepath.Join(dir, name+"-00001-of-00003.gguf"), 100)
	writeSized(t, filepath.Join(dir, name+"-00003-of-00003.gguf"), 4000)

	info := InspectSplit(filepath.Join(dir, name+"-00001-of-00003.gguf"))
	if len(info.Missing) != 1 {
		t.Fatalf("missing = %v, want exactly the absent shard 2", info.Missing)
	}
	if info.TotalBytes != 4100 {
		t.Fatalf("total = %d, want only the shards present", info.TotalBytes)
	}
}

// A plain single-file GGUF is not a split and keeps its own size.
func TestInspectSplit_SingleFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Model-Q8_0.gguf")
	writeSized(t, p, 1234)
	if info := InspectSplit(p); info.IsSplit {
		t.Fatal("a single-file GGUF must not be treated as a split")
	}
	// A "-of-00001" set is likewise not a meaningful split.
	single := filepath.Join(dir, "Model-00001-of-00001.gguf")
	writeSized(t, single, 1234)
	if info := InspectSplit(single); info.IsSplit {
		t.Fatal("a one-shard set must not be treated as a split")
	}
}

// applySplitSize is what makes every consumer (fit, placement, guards) see
// the real size without knowing about splits.
func TestApplySplitSize(t *testing.T) {
	dir := t.TempDir()
	name := "Model"
	shard1 := filepath.Join(dir, name+"-00001-of-00002.gguf")
	writeSized(t, shard1, 50)
	writeSized(t, filepath.Join(dir, name+"-00002-of-00002.gguf"), 9950)

	g := &GGUF{FileSize: 50}
	g.applySplitSize(shard1)
	if g.FileSize != 10000 {
		t.Fatalf("FileSize = %d, want 10000 (the whole set)", g.FileSize)
	}
	if g.SplitCount != 2 {
		t.Fatalf("SplitCount = %d", g.SplitCount)
	}

	// Non-split input leaves the size untouched.
	plain := &GGUF{FileSize: 777}
	plain.applySplitSize(filepath.Join(dir, "Plain.gguf"))
	if plain.FileSize != 777 || plain.SplitCount != 0 {
		t.Fatalf("non-split file was modified: %+v", plain)
	}
}

// WeightBytes must report the whole set once FileSize covers it.
func TestWeightBytes_UsesSplitTotal(t *testing.T) {
	g := &GGUF{ParamCount: 284_000_000_000, FileSize: 90_926_928_288, SplitCount: 3}
	if got := g.WeightBytes(); got != 90_926_928_288 {
		t.Fatalf("WeightBytes = %d, want the full split size", got)
	}
}

// Regression (DeepSeek-V4-Flash, z4 2026-08-07): a GGUF without a
// general.parameter_count key reported a weight size of zero, which the
// offload recommender read as "size unknown" and refused to plan against —
// even though the file size was known all along.
func TestWeightBytes_NoParamCountStillUsesFileSize(t *testing.T) {
	g := &GGUF{FileSize: 90_926_928_288}
	if got := g.WeightBytes(); got != 90_926_928_288 {
		t.Fatalf("WeightBytes = %d, want the file size", got)
	}
}
