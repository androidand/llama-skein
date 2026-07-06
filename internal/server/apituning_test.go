package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/tuning"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

func tuningTestServer(t *testing.T, gfx string, tc *config.TuningConfig) *Server {
	t.Helper()
	cfg := config.Config{Models: map[string]config.ModelConfig{}, Tuning: tc}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	db, err := tuning.LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetTuning(db, gfx, 0x7551)
	return s
}

func TestServer_Tuning_GetReportsVerifiedProfile(t *testing.T) {
	s := tuningTestServer(t, "gfx1201", nil)
	w := getJSON(t, s, "/api/tuning")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var st apicontract.TuningStatus
	json.Unmarshal(w.Body.Bytes(), &st)
	if !st.Enabled {
		t.Error("expected enabled")
	}
	if st.DetectedGfx == nil || *st.DetectedGfx != "gfx1201" {
		t.Errorf("detected_gfx = %v", st.DetectedGfx)
	}
	if st.Profile == nil || !st.Profile.Verified {
		t.Error("expected verified gfx1201 profile")
	}
	if st.Profile.Mtp == nil {
		t.Error("gfx1201 profile should include MTP")
	}
	// provenance: untouched values are 'recommended'
	if st.Provenance == nil || (*st.Provenance)["flash_attn"] != apicontract.Recommended {
		t.Errorf("flash_attn should be recommended, prov=%v", st.Provenance)
	}
}

func TestServer_Tuning_OverrideShowsAsOverride(t *testing.T) {
	fa := false
	s := tuningTestServer(t, "gfx1201", &config.TuningConfig{FlashAttn: &fa})
	w := getJSON(t, s, "/api/tuning")
	var st apicontract.TuningStatus
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Profile == nil || st.Profile.Flags == nil || st.Profile.Flags.FlashAttn == nil || *st.Profile.Flags.FlashAttn {
		t.Error("override flash_attn=false should force off in effective profile")
	}
	if (*st.Provenance)["flash_attn"] != apicontract.Override {
		t.Error("flash_attn provenance should be override")
	}
}

func TestServer_Tuning_DisabledWhenNoGPU(t *testing.T) {
	s := tuningTestServer(t, "", nil) // no detected gfx, no profile
	w := getJSON(t, s, "/api/tuning")
	var st apicontract.TuningStatus
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Enabled {
		t.Error("no GPU + no override should resolve disabled")
	}
}

func TestServer_Tuning_ListProfiles(t *testing.T) {
	s := tuningTestServer(t, "gfx1201", nil)
	w := getJSON(t, s, "/api/tuning/profiles")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp apicontract.TuningProfilesResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Profiles) < 3 {
		t.Errorf("expected >=3 shipped profiles, got %d", len(resp.Profiles))
	}
}
