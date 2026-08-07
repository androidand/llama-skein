package placement

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Profile is a placement that was proven to work on this host: the command
// flags that loaded, what it actually cost, and the inputs that made it
// valid. Reusing one skips both the estimate and the risk on a later launch
// — a hybrid load of a 90 GB model takes minutes, so re-deriving (and
// possibly re-failing) it every time is expensive knowledge thrown away.
type Profile struct {
	// Key inputs — a profile is only valid while all of these still hold.
	ModelPath  string `json:"model_path"`
	ModelSize  int64  `json:"model_size"`
	ModelMtime int64  `json:"model_mtime_unix"`
	Engine     string `json:"engine"`
	VRAMTotal  int    `json:"vram_total_mb"`
	HostTotal  int    `json:"host_total_mb"`
	Ctx        int    `json:"ctx"`

	// What worked.
	Mode      Mode      `json:"mode"`
	NCpuMoe   int       `json:"n_cpu_moe,omitempty"`
	FlagOps   []FlagRec `json:"flags"`
	PerfClass PerfClass `json:"perf_class,omitempty"`

	// What it actually cost, measured rather than estimated.
	PeakVRAMMB  int   `json:"peak_vram_mb,omitempty"`
	PeakHostMB  int   `json:"peak_host_mb,omitempty"`
	LoadSeconds int   `json:"load_seconds,omitempty"`
	RecordedAt  int64 `json:"recorded_at_unix"`
}

// FlagRec is a serializable flag operation. offload.FlagOp is not persisted
// directly so the on-disk format stays independent of that package's shape.
type FlagRec struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	Boolean bool   `json:"boolean,omitempty"`
}

// Key identifies the (model, host, engine, context) combination a profile
// was recorded for. Any change invalidates it: a re-quantized model file, a
// different card, a raised container limit, or a new engine build all change
// what fits.
type Key struct {
	ModelPath  string
	ModelSize  int64
	ModelMtime int64
	Engine     string
	VRAMTotal  int
	HostTotal  int
	Ctx        int
}

func (k Key) matches(p Profile) bool {
	return p.ModelPath == k.ModelPath &&
		p.ModelSize == k.ModelSize &&
		p.ModelMtime == k.ModelMtime &&
		p.Engine == k.Engine &&
		p.VRAMTotal == k.VRAMTotal &&
		p.HostTotal == k.HostTotal &&
		p.Ctx == k.Ctx
}

// ProfileStore persists known-good placements as a JSON map keyed by model
// ID. It is safe for concurrent use and writes atomically.
type ProfileStore struct {
	mu   sync.Mutex
	path string
}

func NewProfileStore(path string) *ProfileStore { return &ProfileStore{path: path} }

// Lookup returns the stored profile for a model when it is still valid for
// key. A mismatch is not an error — it just means the world changed and the
// caller should plan afresh.
func (s *ProfileStore) Lookup(modelID string, key Key) (Profile, bool) {
	if s == nil {
		return Profile{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return Profile{}, false
	}
	p, ok := all[modelID]
	if !ok || !key.matches(p) {
		return Profile{}, false
	}
	return p, true
}

// Record stores a profile, replacing any previous one for the model.
func (s *ProfileStore) Record(modelID string, p Profile) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		all = map[string]Profile{}
	}
	all[modelID] = p
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Forget drops a model's profile — used when the placement it records turns
// out not to work after all.
func (s *ProfileStore) Forget(modelID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.loadLocked()
	if err != nil {
		return
	}
	if _, ok := all[modelID]; !ok {
		return
	}
	delete(all, modelID)
	if data, err := json.MarshalIndent(all, "", "  "); err == nil {
		tmp := s.path + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, s.path)
		}
	}
}

func (s *ProfileStore) loadLocked() (map[string]Profile, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Profile{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string]Profile{}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("corrupt placement profiles at %s: %w", s.path, err)
	}
	return all, nil
}

// ProfileFrom builds a profile from a plan plus its measured cost. ok is
// false when the run is not safe to learn from — see WorthLearning.
func ProfileFrom(key Key, plan Plan, peakVRAMMB, peakHostMB, loadSeconds int, recordedAt int64, policy reserveSource) (Profile, bool) {
	if !WorthLearning(plan, peakVRAMMB, peakHostMB, key, policy) {
		return Profile{}, false
	}
	flags := make([]FlagRec, 0, len(plan.FlagOps))
	for _, op := range plan.FlagOps {
		if op.Remove {
			continue
		}
		flags = append(flags, FlagRec{Name: op.Name, Value: op.Value, Boolean: op.Boolean})
	}
	return Profile{
		ModelPath: key.ModelPath, ModelSize: key.ModelSize, ModelMtime: key.ModelMtime,
		Engine: key.Engine, VRAMTotal: key.VRAMTotal, HostTotal: key.HostTotal, Ctx: key.Ctx,
		Mode: plan.Mode, NCpuMoe: plan.NCpuMoe, FlagOps: flags, PerfClass: plan.PerfClass,
		PeakVRAMMB: peakVRAMMB, PeakHostMB: peakHostMB, LoadSeconds: loadSeconds,
		RecordedAt: recordedAt,
	}, true
}

// reserveSource is the slice of placement policy this package needs to judge
// whether a run left safe margins. Satisfied by config.PlacementConfig.
type reserveSource interface {
	GpuReserveMB(vramTotalMB int) int
	HostReserveMB(effectiveTotalMB int) int
}

// WorthLearning reports whether a successful run should become a known-good
// profile. A run that only just fit is NOT worth learning: reusing it as a
// first candidate would make the barely-survived margins the starting point
// for every future launch, on a host whose free memory varies. We learn what
// worked comfortably, not what merely didn't crash.
func WorthLearning(plan Plan, peakVRAMMB, peakHostMB int, key Key, policy reserveSource) bool {
	if !plan.Confident || plan.Mode == ModeRefuse || plan.Mode == ModeUnknown {
		return false
	}
	if policy == nil || key.VRAMTotal <= 0 {
		return false
	}
	// Peaks are optional (a host without VRAM telemetry reports none); with
	// no measurement there is nothing to verify margins against, so a plan
	// is only learned when at least the GPU peak was observed.
	if peakVRAMMB <= 0 {
		return false
	}
	if peakVRAMMB > key.VRAMTotal-policy.GpuReserveMB(key.VRAMTotal) {
		return false
	}
	if peakHostMB > 0 && key.HostTotal > 0 &&
		peakHostMB > key.HostTotal-policy.HostReserveMB(key.HostTotal) {
		return false
	}
	return true
}
