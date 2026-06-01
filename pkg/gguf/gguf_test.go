package gguf_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/androidand/llama-skein/pkg/gguf"
)

func TestParseBasic(t *testing.T) {
	buf := buildGGUF(t, []kvEntry{
		{"general.architecture", "llama"},
		{"general.name", "test-model"},
		{"general.parameter_count", int64(7000000000)},
		{"llama.context.length", int64(8192)},
		{"llama.embedding.length", int64(4096)},
		{"llama.attention.layer_count", int64(32)},
		{"llama.attention.head_count", int64(32)},
		{"llama.attention.head_count_kv", int64(8)},
		{"llama.feed_forward_length", int64(14336)},
		{"llama.rope.freq_base", float64(10000)},
	})

	g, err := gguf.Parse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if g.Architecture != "llama" {
		t.Errorf("Architecture = %q, want %q", g.Architecture, "llama")
	}
	if g.Name != "test-model" {
		t.Errorf("Name = %q, want %q", g.Name, "test-model")
	}
	if g.ParamCount != 7000000000 {
		t.Errorf("ParamCount = %d, want %d", g.ParamCount, 7000000000)
	}
	if g.ContextLength != 8192 {
		t.Errorf("ContextLength = %d, want %d", g.ContextLength, 8192)
	}
	if g.EmbeddingLength != 4096 {
		t.Errorf("EmbeddingLength = %d, want %d", g.EmbeddingLength, 4096)
	}
	if g.LayerCount != 32 {
		t.Errorf("LayerCount = %d, want %d", g.LayerCount, 32)
	}
	if g.HeadCount != 32 {
		t.Errorf("HeadCount = %d, want %d", g.HeadCount, 32)
	}
	if g.HeadCountKV != 8 {
		t.Errorf("HeadCountKV = %d, want %d", g.HeadCountKV, 8)
	}
	if g.FeedForwardLength != 14336 {
		t.Errorf("FeedForwardLength = %d, want %d", g.FeedForwardLength, 14336)
	}
	if g.RopeFreqBase != 10000 {
		t.Errorf("RopeFreqBase = %v, want %v", g.RopeFreqBase, 10000)
	}
}

func TestParseMoE(t *testing.T) {
	buf := buildGGUF(t, []kvEntry{
		{"general.architecture", "llama"},
		{"general.parameter_count", int64(12000000000)},
		{"llama.attention.layer_count", int64(40)},
		{"llama.attention.head_count", int64(64)},
		{"llama.attention.head_count_kv", int64(8)},
		{"llama.embedding.length", int64(5120)},
		{"llama.expert_count", int64(8)},
		{"llama.expert_used_count", int64(2)},
		{"llama.expert_feed_forward_length", int64(14336)},
		{"llama.expert_shared_feed_forward_length", int64(14336)},
	})

	g, err := gguf.Parse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !g.IsMoE() {
		t.Error("IsMoE = false, want true")
	}
	if g.ExpertCount != 8 {
		t.Errorf("ExpertCount = %d, want %d", g.ExpertCount, 8)
	}
	if g.ExpertUsedCount != 2 {
		t.Errorf("ExpertUsedCount = %d, want %d", g.ExpertUsedCount, 2)
	}
}

func TestParseRopeScaling(t *testing.T) {
	buf := buildGGUF(t, []kvEntry{
		{"general.architecture", "llama"},
		{"llama.rope.scaling.type", "yarn"},
		{"llama.rope.scaling.factor", float64(4)},
		{"llama.rope.scaling.original_length", int64(8192)},
		{"llama.rope.scaling.finetuned", true},
	})

	g, err := gguf.Parse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if g.RopeScaling.Type != "yarn" {
		t.Errorf("RopeScaling.Type = %q, want %q", g.RopeScaling.Type, "yarn")
	}
	if g.RopeScaling.Factor != 4 {
		t.Errorf("RopeScaling.Factor = %v, want %v", g.RopeScaling.Factor, 4)
	}
	if g.RopeScaling.OriginalLength != 8192 {
		t.Errorf("RopeScaling.OriginalLength = %d, want %d", g.RopeScaling.OriginalLength, 8192)
	}
	if !g.RopeScaling.Finetuned {
		t.Error("RopeScaling.Finetuned = false, want true")
	}
}

func TestKVCacheBytes(t *testing.T) {
	g := &gguf.GGUF{
		EmbeddingLength: 4096,
		LayerCount:      32,
		HeadCount:       32,
		HeadCountKV:     8,
	}

	// headDim = 4096/32 = 128
	// kvDims = 128 * 8 = 1024
	// bytesPerEntry = 1024 * 2 * 2 = 4096
	// total = ctxLen * 32 * 4096
	got := g.KVCacheBytes(8192)
	want := int64(8192 * 32 * 4096)
	if got != want {
		t.Errorf("KVCacheBytes(8192) = %d, want %d", got, want)
	}
}

func TestMaxCtxSize(t *testing.T) {
	g := &gguf.GGUF{
		ParamCount:      7000000000,
		EmbeddingLength: 4096,
		LayerCount:      32,
		HeadCount:       32,
		HeadCountKV:     8,
		FileSize:        4000000000, // ~4GB model file
	}

	// 24GB VRAM available
	vramFree := int64(24000000000)
	maxCtx := g.MaxCtxSize(vramFree)
	if maxCtx <= 0 {
		t.Errorf("MaxCtxSize = %d, want > 0", maxCtx)
	}
	// At 24GB VRAM, 4GB weights, ~20GB for KV cache, should support > 10000 context
	t.Logf("MaxCtxSize(24GB VRAM, 7B model) = %d", maxCtx)
}

func TestParseV3(t *testing.T) {
	buf := buildGGUFV3(t, []kvEntry{
		{"general.architecture", "llama"},
		{"general.name", "v3-test"},
		{"general.parameter_count", int64(7000000000)},
		{"llama.context.length", int64(8192)},
	})

	g, err := gguf.Parse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if g.Architecture != "llama" {
		t.Errorf("Architecture = %q, want %q", g.Architecture, "llama")
	}
	if g.Name != "v3-test" {
		t.Errorf("Name = %q, want %q", g.Name, "v3-test")
	}
	if g.ParamCount != 7000000000 {
		t.Errorf("ParamCount = %d, want %d", g.ParamCount, 7000000000)
	}
	if g.ContextLength != 8192 {
		t.Errorf("ContextLength = %d, want %d", g.ContextLength, 8192)
	}
}

func TestNotGGUF(t *testing.T) {
	_, err := gguf.Parse(bytes.NewReader([]byte("NOTGGUF")))
	if err == nil {
		t.Error("expected error for invalid magic, got nil")
	}
}

type kvEntry struct {
	key   string
	value any
}

func buildGGUF(t *testing.T, entries []kvEntry) []byte {
	t.Helper()
	return buildGGUFVersion(t, 2, entries)
}

func buildGGUFV3(t *testing.T, entries []kvEntry) []byte {
	t.Helper()
	return buildGGUFVersion(t, 3, entries)
}

func buildGGUFVersion(t *testing.T, version uint32, entries []kvEntry) []byte {
	t.Helper()
	var buf bytes.Buffer

	// Magic
	buf.WriteString("GGUF")

	// Version
	binary.Write(&buf, binary.LittleEndian, version)

	// Tensor count (0)
	binary.Write(&buf, binary.LittleEndian, uint64(0))

	// KV count
	binary.Write(&buf, binary.LittleEndian, uint64(len(entries)))

	for _, entry := range entries {
		// Key
		writeKeyV(&buf, entry.key, version)

		// Value
		writeValueV(&buf, entry.value, version)
	}

	return buf.Bytes()
}

func writeKey(buf *bytes.Buffer, key string) {
	keyLen := len(key)
	binary.Write(buf, binary.LittleEndian, uint64(keyLen))
	buf.WriteString(key)
}

func writeKeyV(buf *bytes.Buffer, key string, version uint32) {
	keyLen := len(key)
	if version == 2 {
		// v2: 1-byte key length
		buf.WriteByte(byte(keyLen))
	} else {
		// v3: 8-byte key length
		binary.Write(buf, binary.LittleEndian, uint64(keyLen))
	}
	buf.WriteString(key)
}

func writeValue(buf *bytes.Buffer, v any) {
	writeValueV(buf, v, 3)
}

func writeValueV(buf *bytes.Buffer, v any, version uint32) {
	switch val := v.(type) {
	case string:
		// Type: STRING = 8
		binary.Write(buf, binary.LittleEndian, uint32(8))
		strLen := len(val)
		if version == 2 {
			buf.WriteByte(byte(strLen))
		} else {
			binary.Write(buf, binary.LittleEndian, uint64(strLen))
		}
		buf.WriteString(val)

	case int64:
		// Type: I64 = 11
		binary.Write(buf, binary.LittleEndian, uint32(11))
		binary.Write(buf, binary.LittleEndian, val)

	case float64:
		// Type: F64 = 12
		binary.Write(buf, binary.LittleEndian, uint32(12))
		binary.Write(buf, binary.LittleEndian, math.Float64bits(val))

	case bool:
		// Type: BOOL = 7
		binary.Write(buf, binary.LittleEndian, uint32(7))
		if val {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}

	default:
		panic("unsupported type")
	}
}
