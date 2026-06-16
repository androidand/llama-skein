package gguf_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/androidand/llama-skein/pkg/gguf"
)

func TestParseBasic(t *testing.T) {
	buf := buildGGUF(t, []kvEntry{
		{"general.architecture", "llama"},
		{"general.name", "test-model"},
		{"general.parameter_count", int64(7000000000)},
		{"llama.context_length", int64(8192)},
		{"llama.embedding_length", int64(4096)},
		{"llama.block_count", int64(32)},
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
		{"llama.block_count", int64(40)},
		{"llama.attention.head_count", int64(64)},
		{"llama.attention.head_count_kv", int64(8)},
		{"llama.embedding_length", int64(5120)},
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
		{"llama.context_length", int64(8192)},
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

func TestParseArrayMetadata(t *testing.T) {
	// Verify the parser reads array fields with correct byte order:
	// element_type (uint32) then count (uint64) then elements.
	var buf bytes.Buffer
	buf.WriteString("GGUF")
	binary.Write(&buf, binary.LittleEndian, uint32(3)) // version
	binary.Write(&buf, binary.LittleEndian, uint64(0)) // tensor count
	binary.Write(&buf, binary.LittleEndian, uint64(2)) // kv count

	// KV 1: architecture string
	writeKeyV(&buf, "general.architecture", 3)
	binary.Write(&buf, binary.LittleEndian, uint32(8)) // STRING
	binary.Write(&buf, binary.LittleEndian, uint64(5))
	buf.WriteString("llama")

	// KV 2: F32 array — same layout as e.g. rope.dimension_sections in real models
	writeKeyV(&buf, "llama.test.float_array", 3)
	binary.Write(&buf, binary.LittleEndian, uint32(9))                      // ARRAY
	binary.Write(&buf, binary.LittleEndian, uint32(6))                      // elem type: F32
	binary.Write(&buf, binary.LittleEndian, uint64(3))                      // count
	binary.Write(&buf, binary.LittleEndian, math.Float32bits(float32(1.5))) // [0]
	binary.Write(&buf, binary.LittleEndian, math.Float32bits(float32(2.5))) // [1]
	binary.Write(&buf, binary.LittleEndian, math.Float32bits(float32(3.5))) // [2]

	g, err := gguf.Parse(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if g.Architecture != "llama" {
		t.Errorf("Architecture = %q, want llama", g.Architecture)
	}
	arr, ok := g.Metadata["llama.test.float_array"].([]float64)
	if !ok {
		t.Fatalf("metadata[float_array] type = %T, want []float64", g.Metadata["llama.test.float_array"])
	}
	if len(arr) != 3 {
		t.Errorf("array len = %d, want 3", len(arr))
	}
	if arr[0] != float64(float32(1.5)) || arr[1] != float64(float32(2.5)) || arr[2] != float64(float32(3.5)) {
		t.Errorf("array values = %v, want [1.5 2.5 3.5]", arr)
	}
}

func TestNotGGUF(t *testing.T) {
	_, err := gguf.Parse(bytes.NewReader([]byte("NOTGGUF")))
	if err == nil {
		t.Error("expected error for invalid magic, got nil")
	}
}

// hfTinyLlamaURL is a small well-known GGUF on HuggingFace used for integration tests.
// We fetch only the first 4 MB via a range request — enough to cover TinyLlama's full
// metadata section including the 32K-entry tokenizer.ggml.tokens array (~1.3 MB total).
const hfTinyLlamaURL = "https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF/resolve/main/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf"

// downloadGGUFHeader fetches the first 4 MB of a GGUF file from HuggingFace using
// an HTTP range request — enough to contain the full metadata section of any model.
// The test is skipped (not failed) when the network is unavailable or -short is set.
func downloadGGUFHeader(t *testing.T, url string) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping HuggingFace download test with -short")
	}

	const rangeEnd = 4*1024*1024 - 1

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Skipf("build request: %v", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", rangeEnd))

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("network unavailable, skipping HuggingFace test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		t.Skipf("HuggingFace returned HTTP %d, skipping", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return data
}

func TestParseRealFile(t *testing.T) {
	data := downloadGGUFHeader(t, hfTinyLlamaURL)

	g, err := gguf.Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
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
	t.Logf("KVCount: %d", g.KVCount)
	t.Logf("TensorCount: %d", g.TensorCount)

	if g.Architecture != "llama" {
		t.Errorf("Architecture = %q, want %q", g.Architecture, "llama")
	}
	if g.Version != 2 && g.Version != 3 {
		t.Errorf("Version = %d, want 2 or 3", g.Version)
	}
	if g.IsMoE() {
		t.Error("IsMoE = true, want false for TinyLlama")
	}
	if g.ContextLength <= 0 {
		t.Error("ContextLength <= 0")
	}
	// general.parameter_count is optional in GGUF; not all quantizers write it
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
