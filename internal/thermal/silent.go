package thermal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSilentProfile is used by `skein providers silent on` — conservative but effective.
// 65% TDP cap cuts fan noise dramatically on blower GPUs with minimal tokens/s impact
// (~10–20% slower, since LLM inference is memory-bandwidth bound).
var DefaultSilentProfile = Profile{
	PowerLimitPct:     65,
	TempTargetCelsius: 82,
}

// Profile holds the thermal limits applied during silent mode.
type Profile struct {
	PowerLimitPct     int `json:"power_limit_pct" yaml:"power_limit_pct"`
	TempTargetCelsius int `json:"temp_target_celsius" yaml:"temp_target_celsius"`
}

// State is the response shape for GET /api/skein/silent.
type State struct {
	Active            bool    `json:"active"`
	Profile           Profile `json:"profile,omitempty"`
	Schedule          string  `json:"schedule,omitempty"`
	OriginalWatts     int     `json:"original_watts,omitempty"`
	Available         bool    `json:"available"`
	UnavailableReason string  `json:"unavailable_reason,omitempty"`
}

// Manager controls GPU power limits for silent mode.
// Uses sysfs hwmon as primary (no ROCm version dependency); falls back to rocm-smi.
// On non-AMD or permission-denied hosts it is a graceful no-op.
type Manager struct {
	mu                sync.Mutex
	active            bool
	profile           Profile
	schedule          string
	origCapMicrowatts int64
	available         bool
	unavailableReason string
	cancelSchedule    context.CancelFunc
}

func NewManager() *Manager {
	m := &Manager{}
	m.probeAvailability()
	return m
}

func (m *Manager) probeAvailability() {
	// Prefer sysfs — stable across ROCm versions.
	if path := hwmonPath("power1_cap"); path != "" {
		if err := os.WriteFile(path, []byte("0"), 0644); err == nil {
			// Zero is harmless — the driver clamps to min allowed.
			// If write succeeded we have write access.
			m.available = true
			return
		} else if !os.IsPermission(err) {
			// Some other error (bad value etc.) — sysfs exists but wrote weird;
			// still count as available, actual Apply will use the real value.
			m.available = true
			return
		}
	}

	// Fallback: rocm-smi with write capability.
	if _, err := exec.LookPath("rocm-smi"); err == nil {
		out, err := exec.Command("rocm-smi", "--showpower").Output()
		if err == nil && len(out) > 5 {
			m.available = true
			return
		}
	}

	m.available = false
	m.unavailableReason = "no AMD GPU power control (sysfs hwmon not writable, rocm-smi not found or no GPU)"
}

// Apply enables silent mode. Safe to call when already active — updates profile.
func (m *Manager) Apply(profile Profile) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.available {
		m.active = true // track intent even if no-op
		m.profile = profile
		return nil
	}

	if profile.PowerLimitPct > 0 && profile.PowerLimitPct < 100 {
		defaultCap, err := readPowerCapMicrowatts("power1_cap_default")
		if err == nil && defaultCap > 0 {
			if !m.active {
				m.origCapMicrowatts = defaultCap
			}
			target := int64(float64(defaultCap) * float64(profile.PowerLimitPct) / 100.0)
			if werr := writePowerCap(target); werr != nil {
				// sysfs failed — try rocm-smi
				watts := int(float64(defaultCap/1_000_000) * float64(profile.PowerLimitPct) / 100.0)
				if rocmErr := rocmSetPower(watts); rocmErr != nil {
					return fmt.Errorf("power cap failed: sysfs=%v rocm-smi=%v", werr, rocmErr)
				}
			}
		}
	}

	if profile.TempTargetCelsius > 0 {
		// Best-effort; not all GPUs support temp target — ignore error.
		_ = rocmSetTempTarget(profile.TempTargetCelsius)
	}

	m.active = true
	m.profile = profile
	return nil
}

// Restore returns GPU to default settings.
func (m *Manager) Restore() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.active {
		return nil
	}

	if m.available && m.origCapMicrowatts > 0 {
		if err := writePowerCap(m.origCapMicrowatts); err != nil {
			_ = rocmResetPower()
		}
	} else if m.available {
		_ = rocmResetPower()
	}

	m.active = false
	m.origCapMicrowatts = 0
	return nil
}

// StartSchedule spawns a goroutine that applies/restores based on a time window.
// Format: "22:00-08:00" (24h, overnight windows supported).
func (m *Manager) StartSchedule(ctx context.Context, schedule string, profile Profile) {
	m.mu.Lock()
	if m.cancelSchedule != nil {
		m.cancelSchedule()
	}
	schedCtx, cancel := context.WithCancel(ctx)
	m.cancelSchedule = cancel
	m.schedule = schedule
	m.mu.Unlock()

	go func() {
		tick := time.NewTicker(60 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-schedCtx.Done():
				return
			case <-tick.C:
				inWindow := inTimeWindow(schedule)
				m.mu.Lock()
				active := m.active
				m.mu.Unlock()
				if inWindow && !active {
					_ = m.Apply(profile)
				} else if !inWindow && active {
					_ = m.Restore()
				}
			}
		}
	}()
}

// StopSchedule cancels the background schedule goroutine.
func (m *Manager) StopSchedule() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancelSchedule != nil {
		m.cancelSchedule()
		m.cancelSchedule = nil
	}
	m.schedule = ""
}

// GetState returns the current silent mode state.
func (m *Manager) GetState() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	origW := int(m.origCapMicrowatts / 1_000_000)
	return State{
		Active:            m.active,
		Profile:           m.profile,
		Schedule:          m.schedule,
		OriginalWatts:     origW,
		Available:         m.available,
		UnavailableReason: m.unavailableReason,
	}
}

// --- sysfs helpers ---

func hwmonPath(file string) string {
	matches, _ := filepath.Glob("/sys/class/drm/card*/device/hwmon/hwmon*/" + file)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func readPowerCapMicrowatts(file string) (int64, error) {
	path := hwmonPath(file)
	if path == "" {
		return 0, fmt.Errorf("hwmon %s not found", file)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

func writePowerCap(microwatts int64) error {
	path := hwmonPath("power1_cap")
	if path == "" {
		return fmt.Errorf("hwmon power1_cap not found")
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(microwatts, 10)), 0644)
}

// --- rocm-smi helpers ---

func rocmSetPower(watts int) error {
	return exec.Command("rocm-smi", "--setpoweroverdrive", strconv.Itoa(watts)).Run()
}

func rocmResetPower() error {
	return exec.Command("rocm-smi", "--resetpoweroverdrive").Run()
}

func rocmSetTempTarget(celsius int) error {
	return exec.Command("rocm-smi", "--settemptarget", "0", strconv.Itoa(celsius)).Run()
}

// --- schedule parser ---

// inTimeWindow returns true if now is within the given window.
// Handles overnight windows ("22:00-08:00" wraps midnight).
func inTimeWindow(schedule string) bool {
	parts := strings.SplitN(schedule, "-", 2)
	if len(parts) != 2 {
		return false
	}
	now := time.Now()
	start, err1 := parseHHMM(parts[0], now)
	end, err2 := parseHHMM(parts[1], now)
	if err1 != nil || err2 != nil {
		return false
	}
	if start.Before(end) {
		return !now.Before(start) && now.Before(end)
	}
	// Overnight window
	return !now.Before(start) || now.Before(end)
}

func parseHHMM(s string, ref time.Time) (time.Time, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, ref.Location()), nil
}
