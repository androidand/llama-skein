package server

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/pkg/gguf"
)

// parseModelPath extracts the local file path from a model cmd string by
// finding the argument after -m or --model. Returns "" if none found.
func parseModelPath(cmd string) string {
	parts, err := config.SanitizeCommand(cmd)
	if err != nil || len(parts) == 0 {
		return ""
	}
	for i, part := range parts {
		if (part == "-m" || part == "--model") && i+1 < len(parts) {
			return parts[i+1]
		}
		if strings.HasPrefix(part, "--model=") {
			return strings.TrimPrefix(part, "--model=")
		}
	}
	return ""
}

// modelsDir returns the configured models directory, or infers it from model
// cmds by finding the common ancestor of all model file paths.
func (s *Server) modelsDir() string {
	if s.cfg.ModelsDir != "" {
		return s.cfg.ModelsDir
	}
	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var dirs []string
	for _, id := range ids {
		if p := parseModelPath(s.cfg.Models[id].Cmd); p != "" {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	if len(dirs) == 0 {
		return ""
	}
	common := dirs[0]
	for _, d := range dirs[1:] {
		common = commonDirAncestor(common, d)
	}
	return common
}

func commonDirAncestor(a, b string) string {
	aParts := strings.Split(filepath.Clean(a), string(filepath.Separator))
	bParts := strings.Split(filepath.Clean(b), string(filepath.Separator))
	n := len(aParts)
	if len(bParts) < n {
		n = len(bParts)
	}
	common := 0
	for i := 0; i < n; i++ {
		if aParts[i] != bParts[i] {
			break
		}
		common = i + 1
	}
	if common == 0 {
		return string(filepath.Separator)
	}
	return string(filepath.Separator) + filepath.Join(aParts[1:common]...)
}

// modelState returns the current state string and loaded flag for a model ID.
// Resolves aliases before lookup. Returns "stopped"/false if not running.
func (s *Server) modelState(realID string) (state string, loaded bool) {
	running := s.local.RunningModels()
	st, ok := running[realID]
	if !ok {
		return "stopped", false
	}
	switch st {
	case process.StateReady:
		return "ready", true
	case process.StateStarting:
		return "starting", false
	case process.StateStopping:
		return "stopping", false
	case process.StateShutdown:
		return "shutdown", false
	default:
		return "stopped", false
	}
}

// addModelRuntimeHints extracts ctx-size and max-tokens from the model cmd
// and sets them in the record map.
func addModelRuntimeHints(record map[string]any, mc config.ModelConfig) {
	args, err := mc.SanitizedCommand()
	if err != nil {
		return
	}
	if ctxSize, ok := commandFlagInt(args, "--ctx-size", "-c"); ok {
		record["context_length"] = ctxSize
	}
	if maxTokens, ok := commandFlagInt(args, "--n-predict", "-n"); ok {
		record["max_output_tokens"] = maxTokens
	}
}

// addGGUFMetadata reads GGUF headers from the model file and adds them to
// the record under record["meta"]["llamaswap"]["gguf"].
func addGGUFMetadata(record map[string]any, mc config.ModelConfig) {
	ggufPath := parseModelPath(mc.Cmd)
	if ggufPath == "" {
		return
	}
	g, err := gguf.ParseFile(ggufPath)
	if err != nil {
		return
	}

	meta := map[string]any{
		"architecture": g.Architecture,
	}
	if g.Name != "" {
		meta["gguf_name"] = g.Name
	}
	if g.ParamCount > 0 {
		meta["parameter_count"] = g.ParamCount
	}
	if g.ContextLength > 0 {
		meta["context_length"] = g.ContextLength
	}
	if g.EmbeddingLength > 0 {
		meta["embedding_length"] = g.EmbeddingLength
	}
	if g.LayerCount > 0 {
		meta["layer_count"] = g.LayerCount
	}
	if g.HeadCount > 0 {
		meta["head_count"] = g.HeadCount
	}
	if g.HeadCountKV > 0 {
		meta["head_count_kv"] = g.HeadCountKV
	}
	if g.FeedForwardLength > 0 {
		meta["feed_forward_length"] = g.FeedForwardLength
	}
	if g.IsMoE() {
		meta["moe"] = true
		meta["expert_count"] = g.ExpertCount
		if g.ExpertUsedCount > 0 {
			meta["expert_used_count"] = g.ExpertUsedCount
		}
		if g.ExpertFeedForwardLength > 0 {
			meta["expert_feed_forward_length"] = g.ExpertFeedForwardLength
		}
		if g.ExpertSharedFeedForwardLength > 0 {
			meta["expert_shared_feed_forward_length"] = g.ExpertSharedFeedForwardLength
		}
	}
	if g.RopeScaling.Type != "" {
		meta["rope_scaling"] = map[string]any{
			"type":            g.RopeScaling.Type,
			"factor":          g.RopeScaling.Factor,
			"original_length": g.RopeScaling.OriginalLength,
			"finetuned":       g.RopeScaling.Finetuned,
		}
	}
	if g.RopeFreqBase > 0 {
		meta["rope_freq_base"] = g.RopeFreqBase
	}

	if existing, ok := record["meta"]; ok {
		if metaMap, ok := existing.(map[string]any); ok {
			if lsMap, ok := metaMap["llamaswap"].(map[string]any); ok {
				lsMap["gguf"] = meta
			}
		}
	} else {
		record["meta"] = map[string]any{
			"llamaswap": map[string]any{"gguf": meta},
		}
	}
}

func commandFlagInt(args []string, names ...string) (int, bool) {
	for i, arg := range args {
		for _, name := range names {
			if arg == name && i+1 < len(args) {
				return parsePositiveInt(args[i+1])
			}
			if value, ok := strings.CutPrefix(arg, name+"="); ok {
				return parsePositiveInt(value)
			}
		}
	}
	return 0, false
}

func parsePositiveInt(value string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}

// buildCmd constructs a llama-server command for modelPath.
// If extraFlags is non-empty it is appended after the --model argument.
// Otherwise the first existing model's cmd is used as a structural template.
func (s *Server) buildCmd(modelPath, extraFlags string) string {
	if extraFlags != "" {
		return "llama-server --port ${PORT} --model " + modelPath + " " + strings.TrimSpace(extraFlags)
	}
	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		mc := s.cfg.Models[id]
		parts, err := config.SanitizeCommand(mc.Cmd)
		if err != nil || len(parts) == 0 {
			continue
		}
		var rebuilt []string
		pathInserted := false
		skip := false
		for _, p := range parts {
			if skip {
				rebuilt = append(rebuilt, modelPath)
				pathInserted = true
				skip = false
				continue
			}
			if p == "-m" || p == "--model" {
				rebuilt = append(rebuilt, p)
				skip = true
				continue
			}
			if strings.HasPrefix(p, "--model=") {
				rebuilt = append(rebuilt, "--model="+modelPath)
				pathInserted = true
				continue
			}
			rebuilt = append(rebuilt, p)
		}
		if pathInserted {
			return strings.Join(rebuilt, " ")
		}
	}
	return "llama-server --port ${PORT} --model " + modelPath + " --n-gpu-layers 99"
}
