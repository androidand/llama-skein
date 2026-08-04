package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/runtime"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// runtimeBackends is the fixed, ordered set /api/runtime reports over —
// mirrors runtime.BackendLlamaCpp/MLX/VLLM.
var runtimeBackends = []string{runtime.BackendLlamaCpp, runtime.BackendMLX, runtime.BackendVLLM}

// runtimeEngineCmd resolves the engine command runtime.Manager.Detect should
// probe for a backend: the first configured model of that backend's actual
// launch command (so a non-PATH install is found), else "" to fall back to
// the well-known binary name on PATH.
func (s *Server) runtimeEngineCmd(backend string) string {
	for _, mc := range s.cfg.Models {
		if mc.Backend == backend {
			if args, err := mc.SanitizedCommand(); err == nil && len(args) > 0 {
				return args[0]
			}
		}
	}
	return ""
}

func toRuntimeInfo(info runtime.Info) apicontract.RuntimeInfo {
	ri := apicontract.RuntimeInfo{
		Backend:   apicontract.RuntimeInfoBackend(info.Backend),
		Installed: info.Installed,
	}
	if info.Version != "" {
		ri.Version = ptrOf(info.Version)
	}
	if info.Detail != "" {
		ri.Detail = ptrOf(info.Detail)
	}
	return ri
}

// handleListRuntimes implements GET /api/runtime: detection (Phase 1) for
// all three backends, always — this is safe and cheap regardless of
// install/upgrade support.
func (s *Server) handleListRuntimes(w http.ResponseWriter, r *http.Request) {
	infos := make([]apicontract.RuntimeInfo, 0, len(runtimeBackends))
	for _, backend := range runtimeBackends {
		info := runtime.For(backend).Detect(r.Context(), s.runtimeEngineCmd(backend))
		infos = append(infos, toRuntimeInfo(info))
	}
	writeJSON(w, infos)
}

// installerFor returns the backend's Installer, or ok=false if it doesn't
// implement one (currently only llama.cpp — see runtime.ErrUseSystemUpgrade).
func installerFor(backend string) (runtime.Installer, bool) {
	inst, ok := runtime.For(backend).(runtime.Installer)
	return inst, ok
}

func (s *Server) runRuntimeOp(w http.ResponseWriter, r *http.Request, backend string, op func(ctx context.Context, inst runtime.Installer, venvDir string) (runtime.Info, error)) {
	valid := false
	for _, b := range runtimeBackends {
		if b == backend {
			valid = true
			break
		}
	}
	if !valid {
		router.SendResponse(w, r, http.StatusBadRequest, "unknown backend: "+backend)
		return
	}
	inst, ok := installerFor(backend)
	if !ok {
		router.SendResponse(w, r, http.StatusBadRequest, "llama.cpp is managed via POST /api/system/upgrade, not this endpoint")
		return
	}
	var req apicontract.RuntimeInstallRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			router.SendResponse(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	venvDir := ""
	if req.VenvDir != nil {
		venvDir = *req.VenvDir
	}
	info, err := op(r.Context(), inst, venvDir)
	if err != nil {
		if errors.Is(err, runtime.ErrUseSystemUpgrade) {
			router.SendResponse(w, r, http.StatusBadRequest, err.Error())
			return
		}
		router.SendResponse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, toRuntimeInfo(info))
}

// handleInstallRuntime implements POST /api/runtime/{backend}/install.
func (s *Server) handleInstallRuntime(w http.ResponseWriter, r *http.Request, backend string) {
	s.runRuntimeOp(w, r, backend, func(ctx context.Context, inst runtime.Installer, venvDir string) (runtime.Info, error) {
		return inst.Install(ctx, venvDir)
	})
}

// handleUpgradeRuntime implements POST /api/runtime/{backend}/upgrade.
func (s *Server) handleUpgradeRuntime(w http.ResponseWriter, r *http.Request, backend string) {
	s.runRuntimeOp(w, r, backend, func(ctx context.Context, inst runtime.Installer, venvDir string) (runtime.Info, error) {
		return inst.Upgrade(ctx, venvDir)
	})
}

// handleCheckRuntimeHealth implements GET /api/runtime/{backend}/health.
// Health is Detect's own Installed bit for this phase (Detect already runs
// the engine's real version/import check) — a real liveness probe (e.g. a
// spawned model actually responding) is a different, heavier concern
// tracked separately, not faked here.
func (s *Server) handleCheckRuntimeHealth(w http.ResponseWriter, r *http.Request, backend string) {
	found := false
	for _, b := range runtimeBackends {
		if b == backend {
			found = true
			break
		}
	}
	if !found {
		router.SendResponse(w, r, http.StatusBadRequest, "unknown backend: "+backend)
		return
	}
	info := runtime.For(backend).Detect(r.Context(), s.runtimeEngineCmd(backend))
	writeJSON(w, apicontract.RuntimeHealth{
		Backend: apicontract.RuntimeHealthBackend(backend),
		Healthy: info.Installed,
	})
}
