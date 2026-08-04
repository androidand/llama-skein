package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/thermal"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// ProfileStore persists the operator's chosen GPU power / silent-mode
// profile across restarts — the whole point of `/api/skein/config`: set it
// once via the API instead of hand-tuning DPM/APU settings (or editing the
// YAML silent_mode block) after every reboot.
type ProfileStore struct {
	mu   sync.Mutex
	path string
}

// NewProfileStore returns a store backed by the JSON file at path. The file
// is created on first Save; Load reports ok=false until then.
func NewProfileStore(path string) *ProfileStore {
	return &ProfileStore{path: path}
}

// Load reads the saved profile. ok is false when nothing has been saved yet
// (not an error) — callers should fall back to the default template.
func (s *ProfileStore) Load() (profile apicontract.UserProfile, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return apicontract.UserProfile{}, false, nil
	}
	if err != nil {
		return apicontract.UserProfile{}, false, err
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return apicontract.UserProfile{}, false, fmt.Errorf("corrupt profile at %s: %w", s.path, err)
	}
	return profile, true, nil
}

// Save writes the profile atomically (temp file + rename) so a crash or
// concurrent read never observes a half-written file.
func (s *ProfileStore) Save(profile apicontract.UserProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating profile dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".profile-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// defaultUserProfile is the template GET /api/skein/config/default returns:
// thermal's own conservative default, inactive until the operator opts in.
func defaultUserProfile() apicontract.UserProfile {
	return apicontract.UserProfile{
		Name: "Default",
		Power: apicontract.PowerProfile{
			PowerLimitPct:     thermal.DefaultSilentProfile.PowerLimitPct,
			TempTargetCelsius: thermal.DefaultSilentProfile.TempTargetCelsius,
		},
		SilentMode: false,
	}
}

// validateUserProfile enforces the ranges documented on PowerProfile and the
// HH:MM-HH:MM schedule format silent.go's own scheduler expects — reject at
// the API boundary rather than let a bad value reach thermal.Manager.Apply.
func validateUserProfile(p apicontract.UserProfile) error {
	if p.Power.PowerLimitPct < 1 || p.Power.PowerLimitPct > 99 {
		return fmt.Errorf("power.power_limit_pct must be 1-99, got %d", p.Power.PowerLimitPct)
	}
	if p.Power.TempTargetCelsius < 40 || p.Power.TempTargetCelsius > 100 {
		return fmt.Errorf("power.temp_target_celsius must be 40-100, got %d", p.Power.TempTargetCelsius)
	}
	if p.Schedule != nil && strings.TrimSpace(*p.Schedule) != "" {
		if err := validateSchedule(*p.Schedule); err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
	}
	return nil
}

// validateSchedule checks the "HH:MM-HH:MM" format silent.go's inTimeWindow
// parses (overnight windows included — start >= end just means "wraps
// midnight", not an error).
func validateSchedule(schedule string) error {
	parts := strings.SplitN(schedule, "-", 2)
	if len(parts) != 2 {
		return fmt.Errorf("want \"HH:MM-HH:MM\", got %q", schedule)
	}
	for _, p := range parts {
		if _, err := time.Parse("15:04", strings.TrimSpace(p)); err != nil {
			return fmt.Errorf("invalid time %q: %w", p, err)
		}
	}
	return nil
}

// applyProfile drives the already-shipped thermal.Manager from a saved/set
// UserProfile: a schedule starts the background toggler, silent_mode with no
// schedule applies immediately and indefinitely, and silent_mode=false
// restores defaults — stopping any schedule that was previously running.
func (s *Server) applyProfile(profile apicontract.UserProfile) error {
	tp := thermal.Profile{
		PowerLimitPct:     profile.Power.PowerLimitPct,
		TempTargetCelsius: profile.Power.TempTargetCelsius,
	}
	if !profile.SilentMode {
		s.silentMode.StopSchedule()
		return s.silentMode.Restore()
	}
	if profile.Schedule != nil && strings.TrimSpace(*profile.Schedule) != "" {
		s.silentMode.StartSchedule(s.shutdownCtx, *profile.Schedule, tp)
		return nil
	}
	s.silentMode.StopSchedule()
	return s.silentMode.Apply(tp)
}

// profileState builds the GET response: the saved (or default) profile
// alongside the thermal manager's live active/available state.
func (s *Server) profileState() apicontract.UserProfile {
	profile, ok, err := s.profileStore.Load()
	if err != nil || !ok {
		return defaultUserProfile()
	}
	return profile
}

// handleAPIGetProfile implements GET /api/skein/config.
func (s *Server) handleAPIGetProfile(w http.ResponseWriter, r *http.Request) {
	state := s.silentMode.GetState()
	writeJSON(w, apicontract.UserProfileState{
		Active:    state.Active,
		Available: state.Available,
		Profile:   s.profileState(),
	})
}

// handleAPISetProfile implements POST /api/skein/config: validates, applies
// to the live thermal manager, then persists — in that order, so a profile
// that fails to apply is never saved as if it had.
func (s *Server) handleAPISetProfile(w http.ResponseWriter, r *http.Request) {
	var profile apicontract.UserProfile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validateUserProfile(profile); err != nil {
		router.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if profile.SilentMode && !s.silentMode.GetState().Available {
		router.SendResponse(w, r, http.StatusServiceUnavailable, "GPU power control not available on this host")
		return
	}
	if err := s.applyProfile(profile); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, "failed to apply profile: "+err.Error())
		return
	}
	if err := s.profileStore.Save(profile); err != nil {
		router.SendResponse(w, r, http.StatusInternalServerError, "profile applied but failed to save: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	state := s.silentMode.GetState()
	writeJSON(w, apicontract.UserProfileState{
		Active:    state.Active,
		Available: state.Available,
		Profile:   profile,
	})
}

// handleAPIProfileDefault implements GET /api/skein/config/default.
func (s *Server) handleAPIProfileDefault(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, defaultUserProfile())
}
