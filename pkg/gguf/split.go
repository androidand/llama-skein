package gguf

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// splitNameRe matches llama.cpp's split-GGUF naming convention, e.g.
// "Model-00001-of-00003.gguf". gguf-split writes shard 1 with the header and
// distributes tensors across the rest, so shard 1 alone can be a few MB of a
// 90 GB model.
var splitNameRe = regexp.MustCompile(`^(.*)-(\d{5})-of-(\d{5})\.gguf$`)

// SplitInfo describes a split-GGUF file set.
type SplitInfo struct {
	IsSplit    bool
	Index      int   // 1-based shard number of the parsed file
	Count      int   // total shards
	TotalBytes int64 // summed size of every shard present on disk
	Missing    []string
}

// InspectSplit reports whether path is one shard of a split GGUF and, if so,
// the total size of the whole set. Sizing a split model from the shard that
// happens to be opened is badly wrong — shard 1 of DeepSeek-V4-Flash
// UD-IQ2_M is 5 MB of a 91 GB model — so every size decision must be made
// against the total.
func InspectSplit(path string) SplitInfo {
	base := filepath.Base(path)
	m := splitNameRe.FindStringSubmatch(base)
	if m == nil {
		return SplitInfo{}
	}
	idx, err1 := strconv.Atoi(m[2])
	count, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || count <= 1 {
		return SplitInfo{}
	}

	info := SplitInfo{IsSplit: true, Index: idx, Count: count}
	dir := filepath.Dir(path)
	for i := 1; i <= count; i++ {
		shard := filepath.Join(dir, m[1]+"-"+pad5(i)+"-of-"+pad5(count)+".gguf")
		st, err := os.Stat(shard)
		if err != nil {
			info.Missing = append(info.Missing, shard)
			continue
		}
		info.TotalBytes += st.Size()
	}
	return info
}

func pad5(v int) string {
	s := strconv.Itoa(v)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}

// applySplitSize sets FileSize to the whole set's size when path is one
// shard of a split GGUF, and merges every shard's tensor table into this
// one. Called by ParseFile so no consumer has to know about splits.
//
// Merging the tensor tables is not optional. Per-tensor byte sizes are
// derived as FileSize / total_elements, so pairing the whole set's size
// with one shard's tensors inflates every tensor by the split factor — which
// is how a 16-layer expert offload got planned for a model that needed far
// more, and then OOM'd on load.
func (g *GGUF) applySplitSize(path string) {
	info := InspectSplit(path)
	if !info.IsSplit || info.TotalBytes <= 0 {
		return
	}
	g.FileSize = info.TotalBytes
	g.SplitCount = info.Count
	g.SplitMissing = info.Missing

	base := filepath.Base(path)
	m := splitNameRe.FindStringSubmatch(base)
	if m == nil {
		return
	}
	dir := filepath.Dir(path)
	merged := append([]TensorInfo(nil), g.Tensors...)
	for i := 1; i <= info.Count; i++ {
		if i == info.Index {
			continue // already have this shard's tensors
		}
		shard := filepath.Join(dir, m[1]+"-"+pad5(i)+"-of-"+pad5(info.Count)+".gguf")
		other, err := parseHeaderOnly(shard)
		if err != nil {
			// A shard we cannot read leaves the table incomplete; drop it
			// entirely rather than let callers size the model from a
			// partial one.
			g.Tensors = nil
			return
		}
		merged = append(merged, other.Tensors...)
	}
	g.Tensors = merged
}

// parseHeaderOnly reads a GGUF's header and tensor table without the
// split-merge recursion applySplitSize would otherwise trigger.
func parseHeaderOnly(path string) (*GGUF, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}
