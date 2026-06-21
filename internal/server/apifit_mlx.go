package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/androidand/llama-skein/internal/fit"
)

// hfHubDir returns the Hugging Face hub cache directory, honoring HF_HOME and
// HUGGINGFACE_HUB_CACHE before the default ~/.cache/huggingface/hub.
func hfHubDir() string {
	if h := os.Getenv("HUGGINGFACE_HUB_CACHE"); h != "" {
		return h
	}
	if h := os.Getenv("HF_HOME"); h != "" {
		return filepath.Join(h, "hub")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "huggingface", "hub")
}

// resolveMLXShape locates an MLX model in the HF cache by its repo id
// (useModelName, e.g. "mlx-community/Qwen3.5-35B-A3B-4bit"), reads its
// config.json, sums the resident safetensors weight bytes, and builds the
// backend-neutral fit shape.
func resolveMLXShape(repo string) (fit.ModelShape, error) {
	hub := hfHubDir()
	if hub == "" {
		return fit.ModelShape{}, fmt.Errorf("could not determine Hugging Face cache dir")
	}
	repoDir := "models--" + strings.ReplaceAll(repo, "/", "--")
	snapDir := filepath.Join(hub, repoDir, "snapshots")
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		return fit.ModelShape{}, fmt.Errorf("model %q not in HF cache (%s): %w", repo, snapDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		snap := filepath.Join(snapDir, e.Name())
		cfg, err := os.ReadFile(filepath.Join(snap, "config.json"))
		if err != nil {
			continue // not this snapshot
		}
		weights := sumSafetensors(snap)
		if weights <= 0 {
			return fit.ModelShape{}, fmt.Errorf("no safetensors weights found under %s", snap)
		}
		return fit.ShapeFromMLXConfig(cfg, weights)
	}
	return fit.ModelShape{}, fmt.Errorf("no snapshot with config.json under %s", snapDir)
}

// sumSafetensors totals the byte size of every *.safetensors file in a snapshot
// dir, resolving the HF cache symlinks into blobs/ so the real weight size is
// counted (the symlinks themselves are tiny).
func sumSafetensors(dir string) int64 {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.safetensors"))
	var total int64
	for _, m := range matches {
		target := m
		if real, err := filepath.EvalSymlinks(m); err == nil {
			target = real
		}
		if fi, err := os.Stat(target); err == nil {
			total += fi.Size()
		}
	}
	return total
}
