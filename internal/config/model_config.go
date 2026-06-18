package config

import (
	"errors"
	"runtime"
	"strings"
)

const (
	MODEL_CONFIG_DEFAULT_TTL = -1
)

// TimeoutsConfig holds timeout settings for proxy connections
// 0 = no timeout
type TimeoutsConfig struct {
	Connect        int `yaml:"connect"`
	KeepAlive      int `yaml:"keepalive"`
	ResponseHeader int `yaml:"responseHeader"`
	TLSHandshake   int `yaml:"tlsHandshake"`
	ExpectContinue int `yaml:"expectContinue"`
	IdleConn       int `yaml:"idleConn"`
}

// BackendLlamaCpp is the default backend, compatible with llama.cpp's llama-server.
const BackendLlamaCpp = "llamacpp"

// BackendMLX targets mlx_lm.server on Apple Silicon.
const BackendMLX = "mlx"

// BackendVLLM targets vllm serve on NVIDIA (CUDA) or AMD (ROCm) GPU hosts.
const BackendVLLM = "vllm"

type ModelConfig struct {
	Cmd           string   `yaml:"cmd"`
	CmdStop       string   `yaml:"cmdStop"`
	Proxy         string   `yaml:"proxy"`
	Aliases       []string `yaml:"aliases"`
	Env           []string `yaml:"env"`
	CheckEndpoint string   `yaml:"checkEndpoint"`
	UnloadAfter   int      `yaml:"ttl"`
	Unlisted      bool     `yaml:"unlisted"`
	UseModelName  string   `yaml:"useModelName"`

	// Backend selects backend-specific behaviours. Empty string is treated as llamacpp.
	Backend string `yaml:"backend"`

	// #179 for /v1/models
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Limit concurrency of HTTP requests to process
	ConcurrencyLimit int `yaml:"concurrencyLimit"`

	// Model filters see issue #174
	Filters ModelFilters `yaml:"filters"`

	// Macros: see #264
	// Model level macros take precedence over the global macros
	Macros MacroList `yaml:"macros"`

	// Metadata: see #264
	// Arbitrary metadata that can be exposed through the API
	Metadata map[string]any `yaml:"metadata"`

	// override global setting
	SendLoadingState *bool `yaml:"sendLoadingState"`

	// Timeout settings for proxy connections
	Timeouts TimeoutsConfig `yaml:"timeouts"`

	// Copy of HealthCheckTimeout from global config
	HealthCheckTimeout int `yaml:"healthCheckTimeout"`
}

func (m *ModelConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawModelConfig ModelConfig
	defaults := rawModelConfig{
		Cmd:              "",
		CmdStop:          "",
		Proxy:            "http://localhost:${PORT}",
		Aliases:          []string{},
		Env:              []string{},
		CheckEndpoint:    "/health",
		UnloadAfter:      MODEL_CONFIG_DEFAULT_TTL, // use GlobalTTL
		Unlisted:         false,
		UseModelName:     "",
		ConcurrencyLimit: 0,
		Name:             "",
		Description:      "",

		// matches http.DefaultTransport
		Timeouts: TimeoutsConfig{
			Connect:        30,
			KeepAlive:      30,
			ResponseHeader: 0,
			TLSHandshake:   10,
			ExpectContinue: 1,
			IdleConn:       90,
		},
	}

	// the default cmdStop to taskkill /f /t /pid ${PID}
	if runtime.GOOS == "windows" {
		defaults.CmdStop = "taskkill /f /t /pid ${PID}"
	}

	if err := unmarshal(&defaults); err != nil {
		return err
	}

	*m = ModelConfig(defaults)
	return nil
}

func (m *ModelConfig) SanitizedCommand() ([]string, error) {
	return SanitizeCommand(m.Cmd)
}

// IsLlamaCpp returns true when the backend is llama.cpp (the default).
// Use this to gate llama.cpp-specific behaviours such as /slots cancellation.
func (m *ModelConfig) IsLlamaCpp() bool {
	return m.Backend == "" || m.Backend == BackendLlamaCpp
}

// mlxUnsupportedFlags are llama.cpp/llama-server flags that mlx_lm.server does
// not accept. If any reach an MLX command (e.g. a backend-unaware ctx-tuning
// path injects --ctx-size), mlx_lm exits instantly on an argparse error and
// the model appears to "fail to load with no error". Each takes one value.
var mlxUnsupportedFlags = []string{
	"--ctx-size",
	"--cache-type-k",
	"--cache-type-v",
	"--n-gpu-layers",
}

// stripMLXUnsupportedFlags removes any mlxUnsupportedFlags (and their values)
// from a command string. Returns the cleaned command and the list of flags
// that were removed. Defensive: mlx_lm cannot use these, so keeping them only
// crashes the process.
func stripMLXUnsupportedFlags(cmd string) (string, []string) {
	fields := strings.Fields(cmd)
	unsupported := make(map[string]bool, len(mlxUnsupportedFlags))
	for _, f := range mlxUnsupportedFlags {
		unsupported[f] = true
	}
	out := make([]string, 0, len(fields))
	var removed []string
	for i := 0; i < len(fields); i++ {
		if unsupported[fields[i]] {
			removed = append(removed, fields[i])
			i++ // also skip the flag's value
			continue
		}
		out = append(out, fields[i])
	}
	if len(removed) == 0 {
		return cmd, nil
	}
	return strings.Join(out, " "), removed
}

// ModelFilters embeds Filters and adds legacy support for strip_params field
// See issue #174
type ModelFilters struct {
	Filters `yaml:",inline"`
}

func (m *ModelFilters) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawModelFilters ModelFilters
	defaults := rawModelFilters{}

	if err := unmarshal(&defaults); err != nil {
		return err
	}

	// Try to unmarshal with the old field name for backwards compatibility
	if defaults.StripParams == "" {
		var legacy struct {
			StripParams string `yaml:"strip_params"`
		}
		if legacyErr := unmarshal(&legacy); legacyErr != nil {
			return errors.New("failed to unmarshal legacy filters.strip_params: " + legacyErr.Error())
		}
		defaults.StripParams = legacy.StripParams
	}

	*m = ModelFilters(defaults)
	return nil
}

// SanitizedStripParams wraps Filters.SanitizedStripParams for backwards compatibility
// Returns ([]string, error) to match existing API
func (f ModelFilters) SanitizedStripParams() ([]string, error) {
	return f.Filters.SanitizedStripParams(), nil
}
