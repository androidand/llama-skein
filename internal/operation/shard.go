package operation

import (
	"sort"
	"strconv"
	"strings"
)

// ShardInfo is the (index, total) pair parsed from a "-NNNNN-of-MMMMM.gguf"
// filename suffix.
type ShardInfo struct {
	Index uint32
	Total uint32
}

// ParseShardInfo parses "...-NNNNN-of-MMMMM.gguf", ported from llmfit's
// parse_shard_info (llmfit-core/src/providers.rs, commit
// 850e80900a583ebb07f8efeab07589dcfd444d92). Both numbers must be ASCII
// digits, index >= 1, index <= total. Returns ok=false for anything else,
// including a well-formed "-of-" that isn't a shard suffix (e.g. no ".gguf"
// extension, or index 0 / index > total).
func ParseShardInfo(filename string) (ShardInfo, bool) {
	stem, ok := strings.CutSuffix(filename, ".gguf")
	if !ok {
		return ShardInfo{}, false
	}
	ofPos := strings.LastIndex(stem, "-of-")
	if ofPos < 0 {
		return ShardInfo{}, false
	}
	totalStr := stem[ofPos+4:]
	total, ok := parseASCIIDigits(totalStr)
	if !ok {
		return ShardInfo{}, false
	}
	before := stem[:ofPos]
	dashPos := strings.LastIndex(before, "-")
	if dashPos < 0 {
		return ShardInfo{}, false
	}
	index, ok := parseASCIIDigits(before[dashPos+1:])
	if !ok {
		return ShardInfo{}, false
	}
	if index == 0 || index > total {
		return ShardInfo{}, false
	}
	return ShardInfo{Index: index, Total: total}, true
}

func parseASCIIDigits(s string) (uint32, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// shardGroupKey returns the prefix+suffix pair that identifies filename's
// shard set — every shard in the same set shares the same key, and no two
// distinct sets in the same repository do (e.g. a Q4_K_M set and a Q5_K_M
// set never collide even though both end in "...-of-00003.gguf"). Grouping
// logic ported from llmfit's build_gguf_candidates
// (llmfit-core/src/providers.rs, commit
// 850e80900a583ebb07f8efeab07589dcfd444d92); returns ok=false for a
// non-shard filename, same as ParseShardInfo.
func shardGroupKey(filename string) (prefix, suffix string, ok bool) {
	if _, ok := ParseShardInfo(filename); !ok {
		return "", "", false
	}
	stem, _ := strings.CutSuffix(filename, ".gguf")
	ofPos := strings.LastIndex(stem, "-of-")
	before := stem[:ofPos]
	dashPos := strings.LastIndex(before, "-")
	return filename[:dashPos+1], filename[ofPos:], true
}

// GroupShards groups filenames into shard sets by shardGroupKey. A filename
// with no shard suffix forms its own singleton group, keyed by its own full
// name (matching llmfit's build_gguf_candidates behavior of passing
// non-shard files through unchanged). Within each real shard group, files
// are sorted by index.
func GroupShards(filenames []string) map[string][]string {
	type member struct {
		filename string
		index    uint32
	}
	byKey := make(map[string][]member)
	for _, f := range filenames {
		key := f // a non-shard file is its own singleton group, keyed by its own name.
		var index uint32
		if prefix, suffix, ok := shardGroupKey(f); ok {
			key = prefix + "|" + suffix
			info, _ := ParseShardInfo(f)
			index = info.Index
		}
		byKey[key] = append(byKey[key], member{filename: f, index: index})
	}
	groups := make(map[string][]string, len(byKey))
	for key, members := range byKey {
		sort.Slice(members, func(i, j int) bool { return members[i].index < members[j].index })
		names := make([]string, len(members))
		for i, m := range members {
			names[i] = m.filename
		}
		groups[key] = names
	}
	return groups
}

// ShardSetComplete reports whether group contains exactly one file for
// every index from 1 to the set's declared total, with no duplicates or
// gaps. group must be non-empty and every member must be a shard of the
// same set (as GroupShards already guarantees for one of its output
// groups) — mixing shards from different sets, or passing a non-shard
// filename, is a caller error and returns complete=false, total=0.
//
// This check has no equivalent in llmfit: llmfit always scans a live,
// already-complete Hugging Face repository listing, so an incomplete shard
// set was never a case its code needed to detect. It is a genuine gap
// design.md decision 5 requires llama-skein to close on its own — a
// client-submitted install plan can reference a partial set, accidentally
// or otherwise, and that must be caught before an operation is created, not
// discovered partway through a download.
func ShardSetComplete(group []string) (complete bool, total uint32) {
	if len(group) == 0 {
		return false, 0
	}
	seen := make(map[uint32]bool, len(group))
	for _, f := range group {
		info, ok := ParseShardInfo(f)
		if !ok {
			return false, 0
		}
		if total == 0 {
			total = info.Total
		} else if info.Total != total {
			return false, 0 // members disagree on the set's total — not one coherent set.
		}
		if seen[info.Index] {
			return false, 0 // duplicate index.
		}
		seen[info.Index] = true
	}
	if uint32(len(seen)) != total {
		return false, total // missing index somewhere in 1..total.
	}
	return true, total
}
