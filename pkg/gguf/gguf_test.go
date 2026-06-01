package gguf_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
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

func TestParseRealFile(t *testing.T) {
	path := "/Users/andreas/models/gguf/lmstudio-community/Qwen3.6-35B-A3B-GGUF/Qwen3.6-35B-A3B-Q4_K_M.gguf"
	g, err := gguf.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	t.Logf("Version: %d", g.Version)
	t.Logf("Architecture: %s", g.Architecture)
	t.Logf("Name: %s", g.Name)
	t.Logf("ParamCount: %d", g.ParamCount)
	t.Logf("ContextLength: %d", g.ContextLength)
	t.Logf("EmbeddingLength: %d", g.EmbeddingLength)
	t.Logf("LayerCount: %d", g.LayerCount)
	t.Logf("HeadCount: %d", g.HeadCount)
	t.Logf("HeadCountKV: %d", g.HeadCountKV)
	t.Logf("FeedForwardLength: %d", g.FeedForwardLength)
	t.Logf("IsMoE: %v", g.IsMoE())
	t.Logf("ExpertCount: %d", g.ExpertCount)
	t.Logf("ExpertUsedCount: %d", g.ExpertUsedCount)
	t.Logf("ExpertFeedForwardLength: %d", g.ExpertFeedForwardLength)
	t.Logf("ExpertSharedFeedForwardLength: %d", g.ExpertSharedFeedForwardLength)
	t.Logf("FileSize: %d", g.FileSize)
	t.Logf("KVCount: %d", g.KVCount)
	t.Logf("TensorCount: %d", g.TensorCount)

	if g.Architecture != "qwen35moe" {
		t.Errorf("Architecture = %q, want %q", g.Architecture, "qwen35moe")
	}
	if g.Version != 3 {
		t.Errorf("Version = %d, want %d", g.Version, 3)
	}
	if !g.IsMoE() {
		t.Error("IsMoE = false, want true")
	}
	if g.ContextLength <= 0 {
		t.Error("ContextLength <= 0")
	}
	if g.ParamCount <= 0 {
		t.Error("ParamCount <= 0")
	}
}

func TestDebugRealFile(t *testing.T) {
	path := "/Users/andreas/models/gguf/lmstudio-community/Qwen3.6-35B-A3B-GGUF/Qwen3.6-35B-A3B-Q4_K_M.gguf"
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	buf := make([]byte, 4)
	io.ReadFull(f, buf)
	t.Logf("Magic: %s", string(buf))

	version := binary.LittleEndian.Uint32(mustRead(f, 4))
	t.Logf("Version: %d", version)

	tensorCount := binary.LittleEndian.Uint64(mustRead(f, 8))
	t.Logf("Tensor count: %d", tensorCount)

	kvCount := binary.LittleEndian.Uint64(mustRead(f, 8))
	t.Logf("KV count: %d", kvCount)

	for i := uint64(0); i < kvCount; i++ {
		off, _ := f.Seek(0, 1)
		keyLen := binary.LittleEndian.Uint64(mustRead(f, 8))
		keyBuf := mustRead(f, int(keyLen))
		key := string(keyBuf)
		typ := binary.LittleEndian.Uint32(mustRead(f, 4))
		t.Logf("[%d] offset=%d key=%q type=0x%08x", i, off, key, typ)

		// Skip value
		switch gguf.GGUFType(typ) {
		case gguf.GGUFTypeString:
			strLen := binary.LittleEndian.Uint64(mustRead(f, 8))
			mustRead(f, int(strLen))
		case gguf.GGUFTypeBool:
			mustRead(f, 1)
		case gguf.GGUFTypeU8, gguf.GGUFTypeI8:
			mustRead(f, 1)
		case gguf.GGUFTypeU16, gguf.GGUFTypeI16:
			mustRead(f, 2)
		case gguf.GGUFTypeU32, gguf.GGUFTypeI32, gguf.GGUFTypeF32:
			mustRead(f, 4)
		case gguf.GGUFTypeU64, gguf.GGUFTypeI64, gguf.GGUFTypeF64:
			mustRead(f, 8)
		case gguf.GGUFTypeArray:
			count := binary.LittleEndian.Uint64(mustRead(f, 8))
			elemType := binary.LittleEndian.Uint32(mustRead(f, 4))
			t.Logf("    bare array: count=%d, elemType=0x%x", count, elemType)
			// Only print first few, skip rest
			if count > 5 {
				t.Logf("    (too many elements, not dumping)")
			}
			for j := uint64(0); j < count; j++ {
				switch gguf.GGUFType(elemType) {
				case gguf.GGUFTypeString:
					sl := binary.LittleEndian.Uint64(mustRead(f, 8))
					if j < 3 {
						s := string(mustRead(f, int(sl)))
						t.Logf("    [%d] %q", j, s)
					} else {
						mustRead(f, int(sl))
					}
				case gguf.GGUFTypeF32:
					if j < 3 {
						v := binary.LittleEndian.Uint32(mustRead(f, 4))
						t.Logf("    [%d] %f", j, math.Float32frombits(v))
					} else {
						mustRead(f, 4)
					}
				case gguf.GGUFTypeF64:
					if j < 3 {
						v := binary.LittleEndian.Uint64(mustRead(f, 8))
						t.Logf("    [%d] %f", j, math.Float64frombits(v))
					} else {
						mustRead(f, 8)
					}
				case gguf.GGUFTypeI32:
					if j < 3 {
						v := binary.LittleEndian.Uint32(mustRead(f, 4))
						t.Logf("    [%d] %d", j, int32(v))
					} else {
						mustRead(f, 4)
					}
				case gguf.GGUFTypeI64:
					if j < 3 {
						v := binary.LittleEndian.Uint64(mustRead(f, 8))
						t.Logf("    [%d] %d", j, int64(v))
					} else {
						mustRead(f, 8)
					}
				case gguf.GGUFTypeU64:
					if j < 3 {
						v := binary.LittleEndian.Uint64(mustRead(f, 8))
						t.Logf("    [%d] %d", j, v)
					} else {
						mustRead(f, 8)
					}
				default:
					mustRead(f, 1)
				}
			}
		default:
			// Array types: 9 + element_type * 0x10000
			if gguf.GGUFType(typ) >= 0x10000 {
				elemType := gguf.GGUFType(typ >> 16)
				count := binary.LittleEndian.Uint64(mustRead(f, 8))
				t.Logf("    typed array: elemType=0x%x, count=%d", elemType, count)
				for j := uint64(0); j < count; j++ {
					switch elemType {
					case gguf.GGUFTypeString:
						sl := binary.LittleEndian.Uint64(mustRead(f, 8))
						if j < 3 {
							s := string(mustRead(f, int(sl)))
							t.Logf("    [%d] %q", j, s)
						} else {
							mustRead(f, int(sl))
						}
					case gguf.GGUFTypeF32:
						if j < 3 {
							v := binary.LittleEndian.Uint32(mustRead(f, 4))
							t.Logf("    [%d] %f", j, math.Float32frombits(v))
						} else {
							mustRead(f, 4)
						}
					case gguf.GGUFTypeF64:
						if j < 3 {
							v := binary.LittleEndian.Uint64(mustRead(f, 8))
							t.Logf("    [%d] %f", j, math.Float64frombits(v))
						} else {
							mustRead(f, 8)
						}
					case gguf.GGUFTypeI32:
						if j < 3 {
							v := binary.LittleEndian.Uint32(mustRead(f, 4))
							t.Logf("    [%d] %d", j, int32(v))
						} else {
							mustRead(f, 4)
						}
					case gguf.GGUFTypeI64:
						if j < 3 {
							v := binary.LittleEndian.Uint64(mustRead(f, 8))
							t.Logf("    [%d] %d", j, int64(v))
						} else {
							mustRead(f, 8)
						}
					case gguf.GGUFTypeU64:
						if j < 3 {
							v := binary.LittleEndian.Uint64(mustRead(f, 8))
							t.Logf("    [%d] %d", j, v)
						} else {
							mustRead(f, 8)
						}
					default:
						mustRead(f, 1)
					}
				}
			} else {
				t.Fatalf("Unknown type 0x%x for key %q", typ, key)
			}
		}
	}

	// After KV, should be at tensor header start
	off, _ := f.Seek(0, 1)
	t.Logf("Final offset: %d", off)
}

func mustRead(r io.Reader, n int) []byte {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	if err != nil {
		panic(err)
	}
	return buf
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
