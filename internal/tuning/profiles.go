// Package tuning ships an open, curated database of recommended llama-server
// settings per GPU architecture (gfx target) and use-case, plus the mechanism
// to detect the host GPU, resolve the effective profile against user
// overrides, and inject the resulting flags into a model launch command.
//
// Profiles are DEFAULTS, never forced: every value can be overridden or the
// whole feature disabled at runtime (see Override / the /api/tuning routes).
package tuning

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed profiles.yaml
var embeddedProfiles []byte

// UserProfilesFilename is the per-host data file (same schema as the embedded
// database) merged over the built-in profiles at load. Users add GPUs/use-cases
// or override shipped entries without forking.
const UserProfilesFilename = "tuning-profiles.yaml"

// DefaultUseCase is used when none is configured.
const DefaultUseCase = "agentic-single"

// Flags are the additive, safe-to-inject llama-server flags a profile sets.
// Pointer fields distinguish "unset" (defer) from an explicit value.
type Flags struct {
	FlashAttn *bool `yaml:"flash_attn"`
	Parallel  *int  `yaml:"parallel"`
}

// MTP describes speculative-decoding flags applied only to MTP-capable models.
type MTP struct {
	ApplyToMTPModels bool    `yaml:"apply_to_mtp_models"`
	DraftNMax        int     `yaml:"draft_n_max"`
	DraftPMin        float64 `yaml:"draft_p_min"`
}

// Profile is one (gfx, use-case) entry in the tuning database.
type Profile struct {
	Gfx        string   `yaml:"gfx"`
	UseCase    string   `yaml:"usecase"`
	Verified   bool     `yaml:"verified"`
	VerifiedOn string   `yaml:"verified_on,omitempty"`
	Flags      Flags    `yaml:"flags"`
	MTP        *MTP     `yaml:"mtp,omitempty"`
	Notes      string   `yaml:"notes,omitempty"`
	DeviceIDs  []string `yaml:"device_ids,omitempty"`
	Sources    []string `yaml:"sources,omitempty"`
}

// UseCase documents a tuning scenario.
type UseCase struct {
	Description string `yaml:"description"`
	Default     bool   `yaml:"default,omitempty"`
}

// BackendEnv holds process-environment defaults injected into backend
// launches. LinuxGlibc is applied to llama.cpp models on Linux only: glibc
// allocator caps that prevent RSS creep / OOM on long-lived load/unload and
// RAM-offload servers. Recommended defaults, overridable — see BackendEnvFor.
type BackendEnv struct {
	LinuxGlibc map[string]string `yaml:"linux_glibc"`
}

// Database is the parsed tuning file.
type Database struct {
	Version    int                `yaml:"version"`
	UseCases   map[string]UseCase `yaml:"usecases"`
	BackendEnv BackendEnv         `yaml:"backend_env"`
	Profiles   []Profile          `yaml:"profiles"`
}

// LoadProfiles parses the embedded database, then merges a user file from
// configDir (if present and parseable) over it. A user profile with the same
// (gfx, usecase) replaces the shipped one; new keys are added. configDir may
// be "" to load only the embedded database.
func LoadProfiles(configDir string) (*Database, error) {
	db := &Database{}
	if err := yaml.Unmarshal(embeddedProfiles, db); err != nil {
		return nil, fmt.Errorf("parse embedded tuning profiles: %w", err)
	}
	if db.UseCases == nil {
		db.UseCases = map[string]UseCase{}
	}

	if configDir != "" {
		path := filepath.Join(configDir, UserProfilesFilename)
		if raw, err := os.ReadFile(path); err == nil {
			user := &Database{}
			if err := yaml.Unmarshal(raw, user); err != nil {
				return nil, fmt.Errorf("parse user tuning profiles %s: %w", path, err)
			}
			db.merge(user)
		}
		// A missing user file is not an error — the embedded DB stands alone.
	}
	return db, nil
}

// merge overlays user profiles/use-cases onto the receiver. Same (gfx,
// usecase) replaces; new entries append. Use-cases merge by key.
func (db *Database) merge(user *Database) {
	for name, uc := range user.UseCases {
		db.UseCases[name] = uc
	}
	// A user file with a backend_env.linux_glibc map replaces the built-in
	// allocator env wholesale (so operators can retune or empty it).
	if user.BackendEnv.LinuxGlibc != nil {
		db.BackendEnv.LinuxGlibc = user.BackendEnv.LinuxGlibc
	}
	for _, up := range user.Profiles {
		replaced := false
		for i, p := range db.Profiles {
			if p.Gfx == up.Gfx && p.UseCase == up.UseCase {
				db.Profiles[i] = up
				replaced = true
				break
			}
		}
		if !replaced {
			db.Profiles = append(db.Profiles, up)
		}
	}
}

// Lookup returns the profile for (gfx, usecase), or ok=false if none is
// defined. An empty usecase resolves to the database's default use-case.
func (db *Database) Lookup(gfx, usecase string) (Profile, bool) {
	if usecase == "" {
		usecase = db.defaultUseCase()
	}
	for _, p := range db.Profiles {
		if p.Gfx == gfx && p.UseCase == usecase {
			return p, true
		}
	}
	return Profile{}, false
}

func (db *Database) defaultUseCase() string {
	for name, uc := range db.UseCases {
		if uc.Default {
			return name
		}
	}
	return DefaultUseCase
}
