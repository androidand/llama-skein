package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/androidand/llama-skein/internal/thermal"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

func newProfileTestServer(t *testing.T) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Server{
		silentMode:   thermal.NewManager(),
		profileStore: NewProfileStore(filepath.Join(t.TempDir(), "profile.json")),
		shutdownCtx:  ctx,
	}
}

func TestProfileStore_LoadMissing_ReportsNotOK(t *testing.T) {
	store := NewProfileStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	_, ok, err := store.Load()
	if err != nil {
		t.Fatalf("Load() on a missing file must not error, got %v", err)
	}
	if ok {
		t.Fatal("Load() on a missing file must report ok=false")
	}
}

func TestProfileStore_SaveThenLoad_Roundtrips(t *testing.T) {
	store := NewProfileStore(filepath.Join(t.TempDir(), "nested", "profile.json"))
	want := apicontract.UserProfile{
		Name:       "Quiet nights",
		Power:      apicontract.PowerProfile{PowerLimitPct: 65, TempTargetCelsius: 82},
		SilentMode: true,
		Schedule:   ptrOf("22:00-08:00"),
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after Save: ok=%v err=%v", ok, err)
	}
	if got.Name != want.Name || got.SilentMode != want.SilentMode || *got.Schedule != *want.Schedule {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", got, want)
	}
}

func TestValidateUserProfile(t *testing.T) {
	valid := apicontract.UserProfile{Power: apicontract.PowerProfile{PowerLimitPct: 65, TempTargetCelsius: 82}}
	cases := []struct {
		name    string
		mutate  func(*apicontract.UserProfile)
		wantErr bool
	}{
		{"valid as-is", func(p *apicontract.UserProfile) {}, false},
		{"power too low", func(p *apicontract.UserProfile) { p.Power.PowerLimitPct = 0 }, true},
		{"power too high", func(p *apicontract.UserProfile) { p.Power.PowerLimitPct = 100 }, true},
		{"temp too low", func(p *apicontract.UserProfile) { p.Power.TempTargetCelsius = 39 }, true},
		{"temp too high", func(p *apicontract.UserProfile) { p.Power.TempTargetCelsius = 101 }, true},
		{"good schedule", func(p *apicontract.UserProfile) { p.Schedule = ptrOf("22:00-08:00") }, false},
		{"malformed schedule", func(p *apicontract.UserProfile) { p.Schedule = ptrOf("tonight") }, true},
		{"schedule missing dash", func(p *apicontract.UserProfile) { p.Schedule = ptrOf("22:00") }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid
			c.mutate(&p)
			err := validateUserProfile(p)
			if (err != nil) != c.wantErr {
				t.Errorf("validateUserProfile(%+v) error = %v, wantErr %v", p, err, c.wantErr)
			}
		})
	}
}

func TestHandleAPIProfileDefault(t *testing.T) {
	s := newProfileTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/skein/config/default", nil)
	rec := httptest.NewRecorder()
	s.handleAPIProfileDefault(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got apicontract.UserProfile
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SilentMode {
		t.Error("default profile must have silent_mode=false")
	}
	if got.Power.PowerLimitPct != thermal.DefaultSilentProfile.PowerLimitPct {
		t.Errorf("default power_limit_pct = %d, want %d", got.Power.PowerLimitPct, thermal.DefaultSilentProfile.PowerLimitPct)
	}
}

func TestHandleAPIGetProfile_NoneSaved_ReturnsDefault(t *testing.T) {
	s := newProfileTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/skein/config", nil)
	rec := httptest.NewRecorder()
	s.handleAPIGetProfile(rec, req)

	var got apicontract.UserProfileState
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Profile.Name != "Default" {
		t.Errorf("with nothing saved, profile should be the default template, got %+v", got.Profile)
	}
}

// Requesting silent_mode=true on a host with no GPU power control must fail
// loudly (503), not silently accept a preference it can never honor — this
// is the explicit contract behavior, distinct from thermal.Manager.Apply's
// own graceful no-op (used internally by the schedule ticker, which must
// never crash the server just because hardware changed).
func TestHandleAPISetProfile_SilentModeOnUnavailableHost_Returns503(t *testing.T) {
	s := newProfileTestServer(t)
	if s.silentMode.GetState().Available {
		t.Skip("this test host has real GPU power control; nothing to assert here")
	}
	body := apicontract.UserProfile{
		Name:       "Night mode",
		Power:      apicontract.PowerProfile{PowerLimitPct: 65, TempTargetCelsius: 82},
		SilentMode: true,
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/skein/config", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	s.handleAPISetProfile(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := s.profileStore.Load(); ok {
		t.Error("a profile rejected as unavailable must never be persisted")
	}
}

// silent_mode=false never depends on hardware availability — turning silent
// mode OFF (or just saving preferences without enabling it) must always
// succeed and persist, on any host.
func TestHandleAPISetProfile_SilentModeFalse_AlwaysSucceeds(t *testing.T) {
	s := newProfileTestServer(t)
	body := apicontract.UserProfile{
		Name:       "Baseline",
		Power:      apicontract.PowerProfile{PowerLimitPct: 65, TempTargetCelsius: 82},
		SilentMode: false,
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/skein/config", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	s.handleAPISetProfile(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	saved, ok, err := s.profileStore.Load()
	if err != nil || !ok {
		t.Fatalf("profile was not persisted: ok=%v err=%v", ok, err)
	}
	if saved.Name != "Baseline" {
		t.Errorf("persisted profile name = %q, want %q", saved.Name, "Baseline")
	}
}

// applyProfile itself (the plumbing onto thermal.Manager, bypassing the
// handler's 503 contract gate) must track silent_mode=true as active even
// when hardware control is unavailable — Apply's own no-op-but-track-intent
// behavior, exercised directly here since the handler now short-circuits
// before ever reaching it on an unavailable host.
func TestApplyProfile_SilentModeTrue_TracksActiveEvenWhenUnavailable(t *testing.T) {
	s := newProfileTestServer(t)
	if s.silentMode.GetState().Available {
		t.Skip("this test host has real GPU power control; nothing to assert here")
	}
	err := s.applyProfile(apicontract.UserProfile{
		Power:      apicontract.PowerProfile{PowerLimitPct: 65, TempTargetCelsius: 82},
		SilentMode: true,
	})
	if err != nil {
		t.Fatalf("applyProfile: %v", err)
	}
	if !s.silentMode.GetState().Active {
		t.Error("silent_mode=true must be tracked as active even when hardware control is unavailable")
	}
}

func TestHandleAPISetProfile_InvalidPayload_Returns400(t *testing.T) {
	s := newProfileTestServer(t)
	body := apicontract.UserProfile{Power: apicontract.PowerProfile{PowerLimitPct: 500, TempTargetCelsius: 82}}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/skein/config", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	s.handleAPISetProfile(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if _, ok, _ := s.profileStore.Load(); ok {
		t.Error("an invalid profile must never be persisted")
	}
}

func TestHandleAPISetProfile_MalformedJSON_Returns400(t *testing.T) {
	s := newProfileTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/skein/config", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	s.handleAPISetProfile(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAPISetProfile_TurnOff_RestoresAndPersists(t *testing.T) {
	s := newProfileTestServer(t)
	on := apicontract.UserProfile{Power: apicontract.PowerProfile{PowerLimitPct: 65, TempTargetCelsius: 82}, SilentMode: true}
	// Force the ON state directly through the plumbing (bypassing the
	// handler's 503 availability gate, same as
	// TestApplyProfile_SilentModeTrue_TracksActiveEvenWhenUnavailable) so the
	// ON→OFF transition below is real and deterministic on any host.
	if err := s.applyProfile(on); err != nil {
		t.Fatalf("test setup: applyProfile(on): %v", err)
	}
	if err := s.profileStore.Save(on); err != nil {
		t.Fatalf("test setup: Save(on): %v", err)
	}

	off := on
	off.SilentMode = false
	buf, _ := json.Marshal(off)
	req := httptest.NewRequest(http.MethodPost, "/api/skein/config", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	s.handleAPISetProfile(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	if s.silentMode.GetState().Active {
		t.Error("silent_mode=false must restore to inactive")
	}
	saved, ok, _ := s.profileStore.Load()
	if !ok || saved.SilentMode {
		t.Errorf("persisted profile must reflect silent_mode=false, got ok=%v saved=%+v", ok, saved)
	}
}
