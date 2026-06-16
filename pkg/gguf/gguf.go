// Package gguf reads metadata from GGUF model files without loading weights.
package gguf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Magic bytes at the start of a GGUF file.
const magic = "GGUF"

// GGUF version constants.
const (
	Version2 uint32 = 2
	Version3 uint32 = 3
)

const maxMetadataKeyBytes = 64 * 1024
const maxMetadataArrayCount = 100000

// GGUFType identifies the type of a metadata value.
type GGUFType uint32

const (
	GGUFTypeU8     GGUFType = 0
	GGUFTypeI8     GGUFType = 1
	GGUFTypeU16    GGUFType = 2
	GGUFTypeI16    GGUFType = 3
	GGUFTypeU32    GGUFType = 4
	GGUFTypeI32    GGUFType = 5
	GGUFTypeF32    GGUFType = 6
	GGUFTypeBool   GGUFType = 7
	GGUFTypeString GGUFType = 8
	GGUFTypeArray  GGUFType = 9
	GGUFTypeU64    GGUFType = 10
	GGUFTypeI64    GGUFType = 11
	GGUFTypeF64    GGUFType = 12

	// Array types: 9 + element_type * 0x10000
	GGUFTypeU8Array     GGUFType = 9 + 0*0x10000  // 0x00000009
	GGUFTypeI8Array     GGUFType = 9 + 1*0x10000  // 0x00010009
	GGUFTypeU16Array    GGUFType = 9 + 2*0x10000  // 0x00020009
	GGUFTypeI16Array    GGUFType = 9 + 3*0x10000  // 0x00030009
	GGUFTypeU32Array    GGUFType = 9 + 4*0x10000  // 0x00040009
	GGUFTypeI32Array    GGUFType = 9 + 5*0x10000  // 0x00050009
	GGUFTypeF32Array    GGUFType = 9 + 6*0x10000  // 0x00060009
	GGUFTypeStringArray GGUFType = 9 + 8*0x10000  // 0x00080009
	GGUFTypeU64Array    GGUFType = 9 + 10*0x10000 // 0x000A0009
	GGUFTypeI64Array    GGUFType = 9 + 11*0x10000 // 0x000B0009
	GGUFTypeF64Array    GGUFType = 9 + 12*0x10000 // 0x000C0009
)

// GGUF holds the parsed metadata from a GGUF file.
type GGUF struct {
	Version     uint32
	TensorCount uint64
	KVCount     uint64

	// Raw metadata as key-value pairs.
	Metadata map[string]any

	// Parsed convenience fields (derived from metadata).
	Architecture string
	Name         string
	ParamCount   int64
	QuantVersion int64

	// Architecture-specific fields.
	ContextLength     int64
	EmbeddingLength   int64
	LayerCount        int64
	HeadCount         int64
	HeadCountKV       int64
	HeadCountKVGroup  int64
	FeedForwardLength int64
	RopeFreqBase      float64
	RopeScaling       RopeScaling

	// MoE fields (0 means not MoE).
	ExpertCount                   int64
	ExpertUsedCount               int64
	ExpertFeedForwardLength       int64
	ExpertSharedFeedForwardLength int64

	// File size on disk (set by caller, not from GGUF header).
	FileSize int64

	// Tensors holds the tensor info table (name, dims, type) when it could be
	// read. Empty when the tensor section was unavailable or failed to parse;
	// callers fall back to dimensional estimates in that case.
	Tensors []TensorInfo
}

// RopeScaling describes RoPE scaling configuration.
type RopeScaling struct {
	Type           string
	Factor         float64
	OriginalLength int64
	Finetuned      bool
}

// TensorInfo describes a single entry from the GGUF tensor info section.
type TensorInfo struct {
	Name   string
	Dims   []uint64
	Type   uint32 // ggml_type
	Offset uint64
}

// IsMoE returns true if the model uses mixture-of-experts architecture.
func (g *GGUF) IsMoE() bool {
	return g.ExpertCount > 0
}

// WeightBytes estimates the total size of model weights in bytes based on
// parameter count and quantization. This is an approximation.
func (g *GGUF) WeightBytes() int64 {
	if g.ParamCount <= 0 {
		return 0
	}
	// Use file size as the most accurate measure if available.
	if g.FileSize > 0 {
		return g.FileSize
	}
	// Fallback: estimate based on quantization.
	bits := g.bitsPerWeight()
	return (g.ParamCount*int64(bits) + 7) / 8
}

// bitsPerWeight returns the estimated bits per weight based on quantization level.
func (g *GGUF) bitsPerWeight() int {
	arch := g.Architecture
	switch arch {
	case "gemma", "gemma2":
		return 8 // Gemma models are typically F16
	case "phi3", "phi3mini", "phi3v":
		return 8
	}
	return 4 // Default: assume Q4
}

// KVCacheBytes estimates the KV cache size for a given context length.
// This is the dominant memory cost for long context.
func (g *GGUF) KVCacheBytes(ctxLen int64) int64 {
	if g.LayerCount <= 0 || g.EmbeddingLength <= 0 {
		return 0
	}

	// KV cache stores key and value for each layer at each position.
	// Each entry is embedding_length bytes (FP16 for most models).
	// Some architectures use separate key/value dimensions.
	kvDims := g.EmbeddingLength
	if g.HeadCountKV > 0 && g.HeadCount > 0 {
		// For GQA/MQA: KV dims = embedding_length / head_count * head_count_kv * 2
		headDim := g.EmbeddingLength / g.HeadCount
		kvDims = headDim * g.HeadCountKV
	}

	// Key + Value = 2 * kvDims per position per layer.
	// Most llama.cpp models use FP16 (2 bytes) for KV cache.
	bytesPerEntry := kvDims * 2 * 2 // *2 for key+value, *2 for FP16

	return ctxLen * g.LayerCount * bytesPerEntry
}

// VRAMBytes estimates total VRAM needed for the model at a given context length.
// This includes: weights + KV cache + overhead.
// For MoE models, only the shared FFN + active expert FFNs are loaded.
func (g *GGUF) VRAMBytes(ctxLen int64) int64 {
	weights := g.WeightBytes()
	kvCache := g.KVCacheBytes(ctxLen)

	// ~5% overhead for activations, temporary buffers, etc.
	overhead := (weights + kvCache) * 5 / 100

	return weights + kvCache + overhead
}

// MinCtxSize returns the minimum recommended context size for the model.
// Based on the trained context length and architecture.
func (g *GGUF) MinCtxSize() int64 {
	if g.ContextLength > 0 {
		return g.ContextLength
	}
	return 2048 // sensible default
}

// MaxCtxSize returns the maximum practical context size given available VRAM.
// Returns 0 if VRAM is unknown.
func (g *GGUF) MaxCtxSize(vramFreeBytes int64) int64 {
	if vramFreeBytes <= 0 || g.LayerCount <= 0 || g.EmbeddingLength <= 0 {
		return 0
	}

	weights := g.WeightBytes()
	if weights >= vramFreeBytes {
		return 0
	}

	// Available for KV cache = vram - weights - overhead.
	kvBudget := vramFreeBytes - weights - (weights * 5 / 100)
	if kvBudget <= 0 {
		return 0
	}

	kvDims := g.EmbeddingLength
	if g.HeadCountKV > 0 && g.HeadCount > 0 {
		headDim := g.EmbeddingLength / g.HeadCount
		kvDims = headDim * g.HeadCountKV
	}

	bytesPerEntry := kvDims * 2 * 2
	bytesPerCtx := g.LayerCount * bytesPerEntry

	if bytesPerCtx <= 0 {
		return 0
	}

	return kvBudget / bytesPerCtx
}

// Family returns the architecture family name for display.
func (g *GGUF) Family() string {
	return g.Architecture
}

// QuantizationLevel returns a human-readable quantization level string.
func (g *GGUF) QuantizationLevel() string {
	// Try to derive from metadata if available.
	if g.QuantVersion > 0 {
		return fmt.Sprintf("Q%d", g.QuantVersion)
	}
	return ""
}

// ParameterSize returns a human-readable parameter size string (e.g. "7B", "70B").
func (g *GGUF) ParameterSize() string {
	if g.ParamCount <= 0 {
		return ""
	}
	billions := float64(g.ParamCount) / 1e9
	if billions >= 1 {
		return fmt.Sprintf("%.0fB", billions)
	}
	millions := float64(g.ParamCount) / 1e6
	return fmt.Sprintf("%.0fM", millions)
}

var ErrNotGGUF = errors.New("not a GGUF file: invalid magic")
var ErrUnsupportedVersion = errors.New("unsupported GGUF version")

// Parse reads the GGUF header and metadata from the given reader.
// It does not read tensor data — only the metadata section.
func Parse(r io.Reader) (*GGUF, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if string(buf) != magic {
		return nil, ErrNotGGUF
	}

	// Version
	version, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version != Version2 && version != Version3 {
		return nil, fmt.Errorf("%w: got %d", ErrUnsupportedVersion, version)
	}

	// Tensor count
	tensorCount, err := readU64(r)
	if err != nil {
		return nil, fmt.Errorf("read tensor count: %w", err)
	}

	// KV count
	kvCount, err := readU64(r)
	if err != nil {
		return nil, fmt.Errorf("read kv count: %w", err)
	}

	// Read metadata
	metadata := make(map[string]any, kvCount)
	for i := uint64(0); i < kvCount; i++ {
		key, valType, err := readKey(r, version)
		if err != nil {
			return nil, fmt.Errorf("read key %d: %w", i, err)
		}
		val, err := readValue(r, valType, version)
		if err != nil {
			return nil, fmt.Errorf("read value %s: %w", key, err)
		}
		metadata[key] = val
	}

	// The tensor info section follows the metadata. Read it best-effort: a
	// failure here must not break callers that only need metadata, so partial
	// results are discarded and Tensors is left empty.
	tensors, terr := readTensorInfos(r, tensorCount, version)
	if terr != nil {
		tensors = nil
	}

	g := &GGUF{
		Version:     version,
		TensorCount: tensorCount,
		KVCount:     kvCount,
		Metadata:    metadata,
		Tensors:     tensors,
	}

	// Parse convenience fields
	g.Architecture, _ = getString(metadata, "general.architecture")
	g.Name, _ = getString(metadata, "general.name")
	g.ParamCount, _ = getInt64(metadata, "general.parameter_count")
	g.QuantVersion, _ = getInt64(metadata, "general.quantization_version")

	arch := g.Architecture
	if arch == "" {
		arch = "llama" // fallback
	}

	g.ContextLength, _ = getInt64(metadata, arch+".context_length")
	g.EmbeddingLength, _ = getInt64(metadata, arch+".embedding_length")
	g.LayerCount, _ = getInt64(metadata, arch+".block_count")
	g.HeadCount, _ = getInt64(metadata, arch+".attention.head_count")
	g.HeadCountKV, _ = getInt64(metadata, arch+".attention.head_count_kv")
	g.HeadCountKVGroup, _ = getInt64(metadata, arch+".attention.key_head_count_kv_group")
	g.FeedForwardLength, _ = getInt64(metadata, arch+".feed_forward_length")
	g.RopeFreqBase, _ = getFloat64(metadata, arch+".rope.freq_base")

	// Rope scaling
	if scalingType, ok := getString(metadata, arch+".rope.scaling.type"); ok {
		g.RopeScaling.Type = scalingType
		if factor, ok := getFloat64(metadata, arch+".rope.scaling.factor"); ok {
			g.RopeScaling.Factor = factor
		}
		if orig, ok := getInt64(metadata, arch+".rope.scaling.original_length"); ok {
			g.RopeScaling.OriginalLength = orig
		}
		if finetuned, ok := getBool(metadata, arch+".rope.scaling.finetuned"); ok {
			g.RopeScaling.Finetuned = finetuned
		}
	}

	// MoE fields
	g.ExpertCount, _ = getInt64(metadata, arch+".expert_count")
	g.ExpertUsedCount, _ = getInt64(metadata, arch+".expert_used_count")
	g.ExpertFeedForwardLength, _ = getInt64(metadata, arch+".expert_feed_forward_length")
	g.ExpertSharedFeedForwardLength, _ = getInt64(metadata, arch+".expert_shared_feed_forward_length")

	return g, nil
}

// ParseFile reads and parses a GGUF file from disk.
// It also sets the FileSize field.
func ParseFile(path string) (*GGUF, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	g, err := Parse(f)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err == nil {
		g.FileSize = info.Size()
	}

	return g, nil
}

// readKey reads a GGUF metadata key and the following value type.
// GGUF v2: key length is 1 byte (255 means next 8 bytes are the length).
// GGUF v3: key length is always 8 bytes.
func readKey(r io.Reader, version uint32) (string, GGUFType, error) {
	var keyLen uint64
	if version == Version2 {
		b := make([]byte, 1)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", 0, fmt.Errorf("read key length: %w", err)
		}
		if b[0] == 255 {
			buf := make([]byte, 8)
			if _, err := io.ReadFull(r, buf); err != nil {
				return "", 0, fmt.Errorf("read key length: %w", err)
			}
			keyLen = binary.LittleEndian.Uint64(buf)
		} else {
			keyLen = uint64(b[0])
		}
	} else {
		buf := make([]byte, 8)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", 0, fmt.Errorf("read key length: %w", err)
		}
		keyLen = binary.LittleEndian.Uint64(buf)
	}
	if keyLen > maxMetadataKeyBytes {
		return "", 0, fmt.Errorf("metadata key length %d exceeds limit %d", keyLen, maxMetadataKeyBytes)
	}

	keyBuf := make([]byte, keyLen)
	if _, err := io.ReadFull(r, keyBuf); err != nil {
		return "", 0, fmt.Errorf("read key %d bytes: %w", keyLen, err)
	}

	typVal, err := readU32(r)
	if err != nil {
		return "", 0, fmt.Errorf("read value type: %w", err)
	}

	return string(keyBuf), GGUFType(typVal), nil
}

// readValue reads a value of the given GGUFType.
func readValue(r io.Reader, typ GGUFType, version uint32) (any, error) {
	switch typ {
	case GGUFTypeString:
		return readString(r, version)

	case GGUFTypeBool:
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf[0] != 0, nil

	case GGUFTypeU8:
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return int64(buf[0]), nil

	case GGUFTypeI8:
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return int64(int8(buf[0])), nil

	case GGUFTypeI16:
		v, err := readU16(r)
		if err != nil {
			return nil, err
		}
		return int64(int16(v)), nil

	case GGUFTypeI32:
		v, err := readU32(r)
		if err != nil {
			return nil, err
		}
		return int64(int32(v)), nil

	case GGUFTypeI64:
		return readI64(r)

	case GGUFTypeU16:
		v, err := readU16(r)
		if err != nil {
			return nil, err
		}
		return int64(v), nil

	case GGUFTypeU32:
		v, err := readU32(r)
		if err != nil {
			return nil, err
		}
		return int64(v), nil

	case GGUFTypeU64:
		v, err := readU64(r)
		if err != nil {
			return nil, err
		}
		return int64(v), nil

	case GGUFTypeF32:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(buf))), nil

	case GGUFTypeF64:
		buf := make([]byte, 8)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.LittleEndian.Uint64(buf)), nil

	case GGUFTypeArray:
		// GGUF array format: element_type (uint32) then count (uint64) then elements.
		elemTypeBuf := make([]byte, 4)
		if _, err := io.ReadFull(r, elemTypeBuf); err != nil {
			return nil, fmt.Errorf("read array element type: %w", err)
		}
		elemType := GGUFType(binary.LittleEndian.Uint32(elemTypeBuf))
		count, err := readU64(r)
		if err != nil {
			return nil, fmt.Errorf("read array count: %w", err)
		}
		if count > maxMetadataArrayCount {
			return nil, fmt.Errorf("array count %d exceeds limit %d", count, maxMetadataArrayCount)
		}
		switch elemType {
		case GGUFTypeString:
			arr := make([]string, count)
			for i := uint64(0); i < count; i++ {
				arr[i], err = readString(r, version)
				if err != nil {
					return nil, err
				}
			}
			return arr, nil
		case GGUFTypeF32:
			arr := make([]float64, count)
			for i := uint64(0); i < count; i++ {
				buf := make([]byte, 4)
				if _, err := io.ReadFull(r, buf); err != nil {
					return nil, err
				}
				arr[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf)))
			}
			return arr, nil
		case GGUFTypeF64:
			arr := make([]float64, count)
			for i := uint64(0); i < count; i++ {
				buf := make([]byte, 8)
				if _, err := io.ReadFull(r, buf); err != nil {
					return nil, err
				}
				arr[i] = math.Float64frombits(binary.LittleEndian.Uint64(buf))
			}
			return arr, nil
		default:
			arr := make([]int64, count)
			for i := uint64(0); i < count; i++ {
				val, err := readValue(r, elemType, version)
				if err != nil {
					return nil, err
				}
				switch v := val.(type) {
				case int64:
					arr[i] = v
				default:
					return nil, fmt.Errorf("unexpected element type %T in array", val)
				}
			}
			return arr, nil
		}

	default:
		// Array types: 9 + element_type * 0x10000
		if typ >= 0x10000 {
			elemType := GGUFType(typ >> 16)
			count, err := readU64(r)
			if err != nil {
				return nil, fmt.Errorf("read array count: %w", err)
			}
			switch elemType {
			case GGUFTypeString:
				arr := make([]string, count)
				for i := uint64(0); i < count; i++ {
					arr[i], err = readString(r, version)
					if err != nil {
						return nil, err
					}
				}
				return arr, nil
			case GGUFTypeF32:
				arr := make([]float64, count)
				for i := uint64(0); i < count; i++ {
					buf := make([]byte, 4)
					if _, err := io.ReadFull(r, buf); err != nil {
						return nil, err
					}
					arr[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf)))
				}
				return arr, nil
			case GGUFTypeF64:
				arr := make([]float64, count)
				for i := uint64(0); i < count; i++ {
					buf := make([]byte, 8)
					if _, err := io.ReadFull(r, buf); err != nil {
						return nil, err
					}
					arr[i] = math.Float64frombits(binary.LittleEndian.Uint64(buf))
				}
				return arr, nil
			default:
				// Integer array types (U8, I8, U16, I16, U32, I32, U64, I64)
				arr := make([]int64, count)
				for i := uint64(0); i < count; i++ {
					val, err := readValue(r, elemType, version)
					if err != nil {
						return nil, err
					}
					switch v := val.(type) {
					case int64:
						arr[i] = v
					default:
						return nil, fmt.Errorf("unexpected element type %T in array", val)
					}
				}
				return arr, nil
			}
		}
		return nil, fmt.Errorf("unsupported GGUF type: %d", typ)
	}
}

func readString(r io.Reader, version uint32) (string, error) {
	var strLen uint64
	if version == Version2 {
		b := make([]byte, 1)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		if b[0] == 255 {
			lenBuf := make([]byte, 8)
			if _, err := io.ReadFull(r, lenBuf); err != nil {
				return "", err
			}
			strLen = binary.LittleEndian.Uint64(lenBuf)
		} else {
			strLen = uint64(b[0])
		}
	} else {
		lenBuf := make([]byte, 8)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return "", err
		}
		strLen = binary.LittleEndian.Uint64(lenBuf)
	}

	buf := make([]byte, strLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readU16(r io.Reader) (uint16, error) {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf), nil
}

func readU32(r io.Reader) (uint32, error) {
	buf := make([]byte, 4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf), nil
}

func readU64(r io.Reader) (uint64, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf), nil
}

func readI64(r io.Reader) (int64, error) {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf)), nil
}

// maxTensorDims caps tensor dimensionality to guard against corrupt headers.
const maxTensorDims = 8

// readTensorInfos reads the GGUF tensor info section, which follows the metadata
// key-value section. Each entry is: name (gguf string), n_dims (u32),
// dims (n_dims * u64), ggml_type (u32), offset (u64). It does not read tensor
// data. The format is identical across GGUF v2 and v3.
func readTensorInfos(r io.Reader, count uint64, version uint32) ([]TensorInfo, error) {
	if count > maxMetadataArrayCount {
		return nil, fmt.Errorf("tensor count %d exceeds limit %d", count, maxMetadataArrayCount)
	}
	tensors := make([]TensorInfo, 0, count)
	for i := uint64(0); i < count; i++ {
		name, err := readString(r, version)
		if err != nil {
			return nil, fmt.Errorf("read tensor %d name: %w", i, err)
		}
		nDims, err := readU32(r)
		if err != nil {
			return nil, fmt.Errorf("read tensor %q n_dims: %w", name, err)
		}
		if nDims > maxTensorDims {
			return nil, fmt.Errorf("tensor %q has implausible n_dims %d", name, nDims)
		}
		dims := make([]uint64, nDims)
		for d := uint32(0); d < nDims; d++ {
			dims[d], err = readU64(r)
			if err != nil {
				return nil, fmt.Errorf("read tensor %q dim %d: %w", name, d, err)
			}
		}
		typ, err := readU32(r)
		if err != nil {
			return nil, fmt.Errorf("read tensor %q type: %w", name, err)
		}
		offset, err := readU64(r)
		if err != nil {
			return nil, fmt.Errorf("read tensor %q offset: %w", name, err)
		}
		tensors = append(tensors, TensorInfo{Name: name, Dims: dims, Type: typ, Offset: offset})
	}
	return tensors, nil
}

// Helper functions to extract typed values from the metadata map.

func getString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func getInt64(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func getFloat64(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func getBool(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}
