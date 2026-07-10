package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/tuning"
	"github.com/androidand/llama-skein/pkg/apicontract"
	"gopkg.in/yaml.v3"
)

// tuningOverride returns the effective override from the live config.
func (s *Server) tuningOverride() *config.TuningConfig { return s.cfg.Tuning }

// buildTuningStatus resolves the effective profile for this host and renders
// it as the API status. profileFound is false when no profile applies.
func (s *Server) buildTuningStatus() apicontract.TuningStatus {
	status := apicontract.TuningStatus{Enabled: true}
	if s.tunedGfx != "" {
		g := s.tunedGfx
		status.DetectedGfx = &g
	}
	if s.tunedDeviceID != 0 {
		id := fmt.Sprintf("0x%04x", s.tunedDeviceID)
		status.DeviceId = &id
	}
	if s.tuningDB == nil {
		status.Enabled = false
		return status
	}

	eff, gfx, ok := s.tuningDB.EffectiveFor(s.tunedGfx, s.tuningOverride())
	status.Enabled = eff.Enabled
	uc := tuningUseCase(s.tuningOverride())
	status.Usecase = uc
	if len(eff.ExtraArgs) > 0 {
		args := append([]string(nil), eff.ExtraArgs...)
		status.ExtraArgs = &args
	}
	if env := s.tuningDB.BackendEnvFor(s.tuningOverride()); len(env) > 0 {
		status.BackendEnv = &env
	}
	if ok {
		p := toAPIProfile(eff.Profile, gfx, uc)
		status.Profile = &p
		prov := map[string]apicontract.TuningStatusProvenance{}
		for k, v := range eff.Source {
			prov[k] = apicontract.TuningStatusProvenance(v)
		}
		status.Provenance = &prov
	}
	return status
}

func tuningUseCase(tc *config.TuningConfig) string {
	if tc != nil && tc.UseCase != "" {
		return tc.UseCase
	}
	return tuning.DefaultUseCase
}

func toAPIProfile(p tuning.Profile, gfx, usecase string) apicontract.TuningProfile {
	out := apicontract.TuningProfile{
		Gfx:      gfx,
		Usecase:  usecase,
		Verified: p.Verified,
	}
	if p.VerifiedOn != "" {
		out.VerifiedOn = &p.VerifiedOn
	}
	if p.Notes != "" {
		out.Notes = &p.Notes
	}
	if len(p.Sources) > 0 {
		src := append([]string(nil), p.Sources...)
		out.Sources = &src
	}
	if len(p.DeviceIDs) > 0 {
		ids := append([]string(nil), p.DeviceIDs...)
		out.DeviceIds = &ids
	}
	flags := apicontract.TuningFlags{}
	if p.Flags.FlashAttn != nil {
		flags.FlashAttn = p.Flags.FlashAttn
	}
	if p.Flags.Parallel != nil {
		flags.Parallel = p.Flags.Parallel
	}
	out.Flags = &flags
	if p.MTP != nil {
		pmin := float32(p.MTP.DraftPMin)
		out.Mtp = &apicontract.TuningMTP{
			ApplyToMtpModels: &p.MTP.ApplyToMTPModels,
			DraftNMax:        &p.MTP.DraftNMax,
			DraftPMin:        &pmin,
		}
	}
	return out
}

// handleGetTuning implements GET /api/tuning.
func (s *Server) handleGetTuning(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildTuningStatus())
}

// handleListTuningProfiles implements GET /api/tuning/profiles.
func (s *Server) handleListTuningProfiles(w http.ResponseWriter, r *http.Request) {
	resp := apicontract.TuningProfilesResponse{}
	if s.tuningDB != nil {
		for _, p := range s.tuningDB.Profiles {
			resp.Profiles = append(resp.Profiles, toAPIProfile(p, p.Gfx, p.UseCase))
		}
	}
	writeJSON(w, resp)
}

// handlePatchTuning implements PATCH /api/tuning: persist the override to the
// config `tuning:` block and trigger a reload. Present fields are applied;
// omitted fields reset to the recommendation.
func (s *Server) handlePatchTuning(w http.ResponseWriter, r *http.Request) {
	var req apicontract.TuningPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.writeTuningToConfig(req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Reflect immediately in the in-memory config so GET returns the new state
	// before the async reload lands.
	s.cfg.Tuning = patchToConfig(req)
	if s.reloadFn != nil {
		go s.reloadFn()
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, s.buildTuningStatus())
}

func patchToConfig(req apicontract.TuningPatchRequest) *config.TuningConfig {
	tc := &config.TuningConfig{
		Enabled:    req.Enabled,
		FlashAttn:  req.FlashAttn,
		Parallel:   req.Parallel,
		MTP:        req.Mtp,
		GfxTarget:  derefStr(req.GfxTarget),
		BackendEnv: req.BackendEnv,
	}
	if req.ExtraArgs != nil {
		tc.ExtraArgs = *req.ExtraArgs
	}
	return tc
}

// writeTuningToConfig persists the override to the top-level `tuning:` key of
// the config YAML (creating or replacing it), using the same node helpers as
// the other config writers.
func (s *Server) writeTuningToConfig(req apicontract.TuningPatchRequest) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	root, err := readYAMLRoot(s.configFile)
	if err != nil {
		return err
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if req.Enabled != nil {
		yamlMapSet(entry, "enabled", yamlBool(*req.Enabled))
	}
	if req.FlashAttn != nil {
		yamlMapSet(entry, "flash_attn", yamlBool(*req.FlashAttn))
	}
	if req.Parallel != nil {
		yamlMapSet(entry, "parallel", yamlInt(*req.Parallel))
	}
	if req.Mtp != nil {
		yamlMapSet(entry, "mtp", yamlBool(*req.Mtp))
	}
	if req.GfxTarget != nil && *req.GfxTarget != "" {
		yamlMapSet(entry, "gfx_target", yamlScalar(*req.GfxTarget))
	}
	if req.BackendEnv != nil {
		yamlMapSet(entry, "backend_env", yamlBool(*req.BackendEnv))
	}
	if req.ExtraArgs != nil && len(*req.ExtraArgs) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, a := range *req.ExtraArgs {
			seq.Content = append(seq.Content, yamlScalar(a))
		}
		yamlMapSet(entry, "extra_args", seq)
	}
	if len(entry.Content) == 0 {
		// Empty patch → remove the block entirely (reset to recommended).
		yamlMapDelete(root, "tuning")
	} else {
		yamlMapSet(root, "tuning", entry)
	}
	return writeYAMLRoot(s.configFile, root, 0o644)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
