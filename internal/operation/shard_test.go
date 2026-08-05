package operation

import (
	"strings"
	"testing"
)

// Fixtures below mirror llmfit-core/src/providers.rs (commit
// 850e80900a583ebb07f8efeab07589dcfd444d92) so this port stays behaviorally
// identical for the parsing/grouping behavior llmfit itself tests.

func TestParseShardInfo_Smoke(t *testing.T) {
	if _, ok := ParseShardInfo("model-00001-of-00003.gguf"); !ok {
		t.Fatal("expected a shard filename to parse")
	}
	if _, ok := ParseShardInfo("model-Q4_K_M.gguf"); ok {
		t.Fatal("expected a non-shard filename to not parse")
	}
	if _, ok := ParseShardInfo("model.gguf"); ok {
		t.Fatal("expected a plain filename to not parse")
	}
}

func TestParseShardInfo_Basic(t *testing.T) {
	got, ok := ParseShardInfo("Qwen3-Coder-Next-Q5_K_M-00001-of-00003.gguf")
	if !ok || got != (ShardInfo{Index: 1, Total: 3}) {
		t.Fatalf("got %+v, %v; want {1 3}, true", got, ok)
	}
	got, ok = ParseShardInfo("Q5_K_M/Qwen3-Coder-Next-Q5_K_M-00003-of-00003.gguf")
	if !ok || got != (ShardInfo{Index: 3, Total: 3}) {
		t.Fatalf("got %+v, %v; want {3 3}, true", got, ok)
	}
}

func TestParseShardInfo_RejectsNonShards(t *testing.T) {
	cases := map[string]string{
		"model.gguf":                "no shard suffix at all",
		"model-Q4_K_M.gguf":         "a quant suffix, not a shard suffix",
		"model-of-tea.gguf":         `"of" without trailing digits`,
		"model-00001-of-00003.bin":  "wrong extension",
		"model-00004-of-00003.gguf": "index out of range",
		"model-00000-of-00003.gguf": "index zero",
	}
	for filename, reason := range cases {
		t.Run(filename, func(t *testing.T) {
			if _, ok := ParseShardInfo(filename); ok {
				t.Fatalf("%s (%s) should not parse as a shard", filename, reason)
			}
		})
	}
}

func TestGroupShards_GroupsAllShardsOfOneSetSortedByIndex(t *testing.T) {
	files := []string{
		"Q5_K_M/Qwen3-Coder-Next-Q5_K_M-00002-of-00003.gguf",
		"Q5_K_M/Qwen3-Coder-Next-Q5_K_M-00001-of-00003.gguf",
		"Q5_K_M/Qwen3-Coder-Next-Q5_K_M-00003-of-00003.gguf",
		"Q4_K_M/Qwen3-Coder-Next-Q4_K_M.gguf", // unrelated non-shard file in the same listing
	}
	groups := GroupShards(files)
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2 (one shard set + one singleton)", len(groups))
	}
	var shardGroup []string
	for _, members := range groups {
		if len(members) == 3 {
			shardGroup = members
		}
	}
	if shardGroup == nil {
		t.Fatal("no 3-member group found")
	}
	for i, want := range []string{"00001-of-00003", "00002-of-00003", "00003-of-00003"} {
		if !strings.Contains(shardGroup[i], want) {
			t.Fatalf("shardGroup[%d] = %q, want it to contain %q", i, shardGroup[i], want)
		}
	}
}

func TestGroupShards_DoesNotMixDistinctShardSets(t *testing.T) {
	files := []string{
		"Q4_K_M/m-Q4_K_M-00001-of-00002.gguf",
		"Q4_K_M/m-Q4_K_M-00002-of-00002.gguf",
		"Q5_K_M/m-Q5_K_M-00001-of-00003.gguf",
		"Q5_K_M/m-Q5_K_M-00002-of-00003.gguf",
		"Q5_K_M/m-Q5_K_M-00003-of-00003.gguf",
	}
	groups := GroupShards(files)
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2 distinct shard sets", len(groups))
	}
	sizes := map[int]bool{}
	for _, members := range groups {
		sizes[len(members)] = true
	}
	if !sizes[2] || !sizes[3] {
		t.Fatalf("group sizes = %v, want one of size 2 and one of size 3", sizes)
	}
}

func TestGroupShards_NonShardFileIsItsOwnGroup(t *testing.T) {
	groups := GroupShards([]string{"model-Q4_K_M.gguf"})
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(groups))
	}
	for _, members := range groups {
		if len(members) != 1 || members[0] != "model-Q4_K_M.gguf" {
			t.Fatalf("members = %v, want [\"model-Q4_K_M.gguf\"]", members)
		}
	}
}

// ShardSetComplete has no llmfit equivalent — see its doc comment for why.

func TestShardSetComplete_TrueForAFullSet(t *testing.T) {
	group := []string{
		"m-Q5_K_M-00001-of-00003.gguf",
		"m-Q5_K_M-00002-of-00003.gguf",
		"m-Q5_K_M-00003-of-00003.gguf",
	}
	complete, total := ShardSetComplete(group)
	if !complete || total != 3 {
		t.Fatalf("complete=%v total=%d, want true, 3", complete, total)
	}
}

func TestShardSetComplete_FalseForAMissingShard(t *testing.T) {
	group := []string{
		"m-Q5_K_M-00001-of-00003.gguf",
		"m-Q5_K_M-00003-of-00003.gguf", // 00002 missing
	}
	complete, total := ShardSetComplete(group)
	if complete || total != 3 {
		t.Fatalf("complete=%v total=%d, want false, 3", complete, total)
	}
}

func TestShardSetComplete_FalseForADuplicateIndex(t *testing.T) {
	group := []string{
		"m-Q5_K_M-00001-of-00003.gguf",
		"m-Q5_K_M-00001-of-00003.gguf",
		"m-Q5_K_M-00003-of-00003.gguf",
	}
	complete, _ := ShardSetComplete(group)
	if complete {
		t.Fatal("a duplicate index should never be reported complete")
	}
}

func TestShardSetComplete_FalseForMismatchedTotals(t *testing.T) {
	// Members claiming different "total" values are not one coherent set,
	// even if their indices happen to look plausible individually.
	group := []string{
		"m-Q5_K_M-00001-of-00003.gguf",
		"m-Q5_K_M-00002-of-00002.gguf",
	}
	complete, _ := ShardSetComplete(group)
	if complete {
		t.Fatal("mismatched shard totals should never be reported complete")
	}
}

func TestShardSetComplete_FalseForEmptyOrNonShardInput(t *testing.T) {
	if complete, _ := ShardSetComplete(nil); complete {
		t.Fatal("an empty group should never be complete")
	}
	if complete, _ := ShardSetComplete([]string{"model-Q4_K_M.gguf"}); complete {
		t.Fatal("a non-shard filename should never be reported complete")
	}
}
