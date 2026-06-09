// Package api provides canonical Go types for every llama-skein REST response.
// Skein imports these types directly instead of maintaining parallel struct
// definitions. This package has zero dependencies on llama-skein internals.
package api

import "time"

// Model represents a model in llama-skein's model list.
type Model struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Unlisted    bool     `json:"unlisted"`
	PeerID      string   `json:"peerID,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// ResourceSnapshot is the response envelope for GET /api/resources.
type ResourceSnapshot struct {
	GPUs    []GPUSnapshot `json:"gpus,omitempty"`
	VRAM    VRAMInfo      `json:"vram,omitempty"`
	CPU     CPUInfo       `json:"cpu,omitempty"`
	Memory  MemoryInfo    `json:"memory,omitempty"`
	Storage StorageInfo   `json:"storage,omitempty"`
}

// GPUSnapshot represents a single GPU's latest state.
type GPUSnapshot struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	VRAMTotalMB    int     `json:"vram_total_mb"`
	VRAMUsedMB     int     `json:"vram_used_mb"`
	VRAMFreeMB     int     `json:"vram_free_mb"`
	UtilizationPct float64 `json:"utilization_pct"`
	TempC          int     `json:"temp_c"`
	PowerDrawW     float64 `json:"power_draw_w"`
}

// VRAMInfo aggregates VRAM across all GPUs.
type VRAMInfo struct {
	TotalMB int `json:"total_mb"`
	UsedMB  int `json:"used_mb"`
	FreeMB  int `json:"free_mb"`
}

// CPUInfo represents the latest CPU snapshot.
type CPUInfo struct {
	Cores       int       `json:"cores"`
	UtilAvgPct  float64   `json:"util_avg_pct"`
	UtilPerCore []float64 `json:"util_per_core"`
	LoadAvg1    float64   `json:"load_avg_1"`
	LoadAvg5    float64   `json:"load_avg_5"`
	LoadAvg15   float64   `json:"load_avg_15"`
}

// MemoryInfo represents system memory state.
type MemoryInfo struct {
	TotalMB   int     `json:"total_mb"`
	UsedMB    int     `json:"used_mb"`
	FreeMB    int     `json:"free_mb"`
	SwapTotal int     `json:"swap_total"`
	SwapUsed  int     `json:"swap_used"`
	Type      string  `json:"type"`
	LoadAvg1  float64 `json:"load_avg_1"`
	LoadAvg5  float64 `json:"load_avg_5"`
	LoadAvg15 float64 `json:"load_avg_15"`
}

// StorageInfo represents disk storage for models directory.
type StorageInfo struct {
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	ModelsDir  string `json:"models_dir"`
}

// VersionInfo represents GET /api/version response.
type VersionInfo struct {
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	BuildDate     string   `json:"build_date"`
	LlamaCppBuild string   `json:"llama_cpp_build"`
	LlamaCppGit   string   `json:"llama_cpp_git"`
	LlamaCppDate  string   `json:"llama_cpp_date"`
	BuildFeatures []string `json:"build_features"`
	Runtime       ginH     `json:"runtime"`
}

// ginH is a minimal map type to avoid importing gin.
type ginH map[string]interface{}

// StorageSnapshot represents GET /api/storage response.
type StorageSnapshot struct {
	ModelsDir string            `json:"models_dir"`
	Stats     map[string]uint64 `json:"stats"`
}

// PullProgress represents streaming progress for POST /api/models/pull.
type PullProgress struct {
	Status    string `json:"status"`
	Filename  string `json:"filename,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ModelLoadError represents a model loading failure.
type ModelLoadError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	UpstreamError string `json:"upstream_error"`
	ExitHint      string `json:"exit_hint"`
	When          string `json:"at"`
}

// ActivityLogEntry represents a metrics/activity log entry.
type ActivityLogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Model        string    `json:"model"`
	DurationMs   float64   `json:"duration_ms"`
	Tokens       int       `json:"tokens"`
	TokensPerSec float64   `json:"tokens_per_second"`
}

// MetricsSnapshot is the response for GET /api/metrics.
type MetricsSnapshot struct {
	Metrics []ActivityLogEntry `json:"metrics"`
}

// ConfigInfo is the response for GET /api/config/info.
type ConfigInfo struct {
	ConfigPath string `json:"config_path"`
	Exists     bool   `json:"exists"`
}

// ModelConfigResponse represents GET /api/config/models/:id.
type ModelConfigResponse struct {
	ModelID string      `json:"id"`
	Config  ModelConfig `json:"config"`
}

// ModelConfig is the public-facing model configuration.
type ModelConfig struct {
	Backend          string         `yaml:"backend" json:"backend"`
	Cmd              string         `yaml:"cmd" json:"cmd"`
	CmdStop          string         `yaml:"cmdStop" json:"cmdStop"`
	Proxy            string         `yaml:"proxy" json:"proxy"`
	Aliases          []string       `yaml:"aliases" json:"aliases"`
	Env              []string       `yaml:"env" json:"env"`
	CheckEndpoint    string         `yaml:"checkEndpoint" json:"checkEndpoint"`
	UnloadAfter      int            `yaml:"ttl" json:"ttl"`
	Unlisted         bool           `yaml:"unlisted" json:"unlisted"`
	UseModelName     string         `yaml:"useModelName" json:"useModelName"`
	Name             string         `yaml:"name" json:"name"`
	Description      string         `yaml:"description" json:"description"`
	ConcurrencyLimit int            `yaml:"concurrencyLimit" json:"concurrencyLimit"`
	Metadata         map[string]any `yaml:"metadata" json:"metadata"`
}

// LlamaCppBuildInfo carries llama.cpp build metadata from the fork's ldflags injection.
type LlamaCppBuildInfo struct {
	Build         string   `json:"llama_cpp_build"`
	Git           string   `json:"llama_cpp_git"`
	Date          string   `json:"llama_cpp_date"`
	BuildFeatures []string `json:"build_features"`
}
