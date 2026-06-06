package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/androidand/llama-skein/internal/chain"
	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/perf"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/thermal"
	"github.com/androidand/llama-skein/pkg/api"
)

// Server owns the HTTP mux, cross-cutting middleware, and the local/peer model
// dispatch. It supersedes router.Server: it builds the local and peer routers
// directly and dispatches between them itself.
type Server struct {
	cfg config.Config

	muxlog      *logmon.Monitor
	proxylog    *logmon.Monitor
	upstreamlog *logmon.Monitor

	perf       *perf.Monitor
	inflight   *inflightCounter
	metrics    *metricsMonitor
	silentMode *thermal.Manager
	build      BuildInfo

	local router.LocalRouter
	peer  router.Router

	mux     *http.ServeMux
	handler http.Handler

	shutdownCtx  context.Context
	shutdownFn   context.CancelFunc
	shuttingDown atomic.Bool

	// fork-specific: config file management and mDNS
	configFile string
	configMu   sync.Mutex
	reloadFn   func()
}

// SetConfigFile stores the on-disk config path so the management API can write
// model entries back to it.
func (s *Server) SetConfigFile(path string) { s.configFile = path }

// SetReloadFn injects the reload callback so POST /api/config/reload can
// trigger it.
func (s *Server) SetReloadFn(fn func()) { s.reloadFn = fn }

// modelPostJSONRoutes are endpoints with a model id in the JSON request body.
var modelPostJSONRoutes = []string{
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/completions",
	"/v1/messages",
	"/v1/messages/count_tokens",
	"/v1/embeddings",
	"/reranking",
	"/rerank",
	"/v1/rerank",
	"/v1/reranking",
	"/infill",
	"/completion",
	"/v1/audio/speech",
	"/v1/audio/voices",
	"/v1/images/generations",
	"/sdapi/v1/txt2img",
	"/sdapi/v1/img2img",

	// versionless routes, the /v/ is stripped before the request is forwarded upstream
	// see issue #728
	"/v/chat/completions",
	"/v/responses",
	"/v/completions",
	"/v/messages",
	"/v/messages/count_tokens",
	"/v/embeddings",
	"/v/rerank",
	"/v/reranking",
}

// modelPostFormRoutes are multipart/form-data endpoints with a model id in the form data
var modelPostFormRoutes = []string{
	"/v1/audio/transcriptions",
	"/v1/images/edits",
}

// modelGetRoutes are model-dispatched GET endpoints (the model arrives as a
// query parameter).
var modelGetRoutes = []string{
	"/v1/audio/voices",
	"/sdapi/v1/loras",
}

// BuildInfo carries version metadata surfaced by GET /api/version.
type BuildInfo struct {
	Version         string
	Commit          string
	Date            string
	UpstreamVersion string
	SkeinVersion    string
	LlamaCppBuild   string
	LlamaCppGit     string
	LlamaCppDate    string
	BuildFeatures   string
}

func New(cfg config.Config, muxlog *logmon.Monitor, proxylog *logmon.Monitor, upstreamlog *logmon.Monitor, perfMon *perf.Monitor, build BuildInfo) (*Server, error) {
	var local router.LocalRouter
	var err error

	if cfg.Matrix != nil {
		local, err = router.NewMatrix(cfg, proxylog, upstreamlog)
		if err != nil {
			return nil, fmt.Errorf("creating matrix router: %w", err)
		}
	} else {
		local, err = router.NewGroup(cfg, proxylog, upstreamlog)
		if err != nil {
			return nil, fmt.Errorf("creating group router: %w", err)
		}
	}

	peer, err := router.NewPeer(cfg, proxylog)
	if err != nil {
		return nil, fmt.Errorf("creating peer router: %w", err)
	}

	silentMgr := thermal.NewManager()

	shutdownCtx, shutdownFn := context.WithCancel(context.Background())
	s := &Server{
		cfg:         cfg,
		muxlog:      muxlog,
		proxylog:    proxylog,
		upstreamlog: upstreamlog,
		perf:        perfMon,
		inflight:    &inflightCounter{},
		metrics:     newMetricsMonitor(proxylog, cfg.MetricsMaxInMemory, cfg.CaptureBuffer),
		silentMode:  silentMgr,
		build:       build,
		local:       local,
		peer:        peer,
		shutdownCtx: shutdownCtx,
		shutdownFn:  shutdownFn,
	}

	if sched := cfg.SilentMode.Schedule; sched != "" {
		pct := cfg.SilentMode.PowerLimitPct
		if pct == 0 {
			pct = thermal.DefaultSilentProfile.PowerLimitPct
		}
		tmp := cfg.SilentMode.TempTargetCelsius
		if tmp == 0 {
			tmp = thermal.DefaultSilentProfile.TempTargetCelsius
		}
		silentMgr.StartSchedule(shutdownCtx, sched, thermal.Profile{
			PowerLimitPct:     pct,
			TempTargetCelsius: tmp,
		})
	}

	s.routes()
	s.startPreload()
	return s, nil
}

// localPeerHandler dispatches a model-routed request to the local or peer
// router. The model is resolved once via router.FetchContext.
func (s *Server) localPeerHandler(w http.ResponseWriter, r *http.Request) {
	stripVersionPrefix(r)

	data, err := router.FetchContext(r, s.cfg)
	if err != nil {
		router.SendError(w, r, router.ErrNoModelInContext)
		return
	}

	switch {
	case s.local.Handles(data.ModelID):
		s.proxylog.Debugf("dispatch: using local process for model: %s", data.ModelID)
		s.local.ServeHTTP(w, r)
	case s.peer.Handles(data.ModelID):
		s.proxylog.Debugf("dispatch: using peer for model: %s", data.ModelID)
		s.peer.ServeHTTP(w, r)
	default:
		router.SendError(w, r, router.ErrNoRouterFound)
	}
}

// stripVersionPrefix rewrites versionless /v/... requests to their /... form
// before forwarding upstream (issue #728).
func stripVersionPrefix(r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v/") {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v")
	}
}

// routes builds the mux, registers every route, and wraps the mux with the
// global CORS middleware.
func (s *Server) routes() {
	authMW := CreateAuthMiddleware(s.cfg)
	filterMW := CreateFilterMiddleware(s.cfg)
	formFilterMW := CreateFormFilterMiddleware(s.cfg)

	// Model-dispatched routes get auth + per-model concurrency limiting + body
	// filters + in-flight tracking + token metrics. concurrencyMW rejects with
	// 429 before the body filters do any rewrite work. filterMW rewrites JSON
	// bodies and formFilterMW rewrites multipart bodies; each is a no-op for the
	// other's Content-Type. Both run before the metrics middleware so it buffers
	// the rewritten body.
	modelChain := chain.New(
		authMW,
		CreateConcurrencyMiddleware(s.cfg),
		filterMW,
		formFilterMW,
		CreateInflightMiddleware(s.inflight),
		CreateMetricsMiddleware(s.metrics, s.cfg),
	)
	// Custom endpoints only need auth.
	apiChain := chain.New(authMW)

	mux := http.NewServeMux()
	dispatch := http.HandlerFunc(s.localPeerHandler)

	for _, path := range modelPostJSONRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelPostFormRoutes {
		mux.Handle("POST "+path, modelChain.Then(dispatch))
	}
	for _, path := range modelGetRoutes {
		mux.Handle("GET "+path, modelChain.Then(dispatch))
	}

	// llama-swap API + custom endpoints.
	mux.Handle("GET /v1/models", apiChain.ThenFunc(s.handleListModels))
	mux.Handle("GET /logs", apiChain.ThenFunc(s.handleLogs))
	mux.Handle("GET /logs/stream", apiChain.ThenFunc(s.handleLogStream))
	mux.Handle("GET /logs/stream/{logMonitorID...}", apiChain.ThenFunc(s.handleLogStream))

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /wol-health", handleHealth)
	mux.HandleFunc("GET /{$}", handleRootRedirect)

	// Embedded UI.
	mux.HandleFunc("GET /ui/", s.handleUI)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)

	// Prometheus metrics (no auth, matches the legacy endpoint).
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	// Operations endpoints.
	mux.Handle("GET /unload", apiChain.ThenFunc(s.handleUnload))
	mux.Handle("GET /running", apiChain.ThenFunc(s.handleRunning))

	// Upstream passthrough.
	mux.HandleFunc("GET /upstream", handleUpstreamRedirect)
	mux.Handle("/upstream/{upstreamPath...}", apiChain.ThenFunc(s.handleUpstream))

	// Models — lifecycle management.
	mux.Handle("GET /api/models", apiChain.ThenFunc(s.handleAPIListModels))
	mux.Handle("GET /api/models/context/{model...}", apiChain.ThenFunc(s.handleAPIContextRecommendation))
	mux.Handle("GET /api/models/{model...}", apiChain.ThenFunc(s.handleAPIGetModel))
	mux.Handle("DELETE /api/models/{model...}", apiChain.ThenFunc(s.handleAPIDeleteModel))
	mux.Handle("POST /api/models/load/{model...}", apiChain.ThenFunc(s.handleAPILoadModel))
	mux.Handle("POST /api/models/unload", apiChain.ThenFunc(s.handleAPIUnloadAll))
	mux.Handle("POST /api/models/unload/{model...}", apiChain.ThenFunc(s.handleAPIUnloadModel))
	mux.Handle("POST /api/models/pull", apiChain.ThenFunc(s.handleAPIPullModel))

	// Config — live YAML management.
	mux.Handle("GET /api/config/info", apiChain.ThenFunc(s.handleAPIConfigInfo))
	mux.Handle("POST /api/config/models", apiChain.ThenFunc(s.handleAPIConfigAddModel))
	mux.Handle("GET /api/config/models/{id}", apiChain.ThenFunc(s.handleAPIConfigGetModel))
	mux.Handle("PATCH /api/config/models/{id}", apiChain.ThenFunc(s.handleAPIConfigPatchModel))
	mux.Handle("DELETE /api/config/models/{id}", apiChain.ThenFunc(s.handleAPIConfigRemoveModel))
	mux.Handle("PATCH /api/config/groups/{id}", apiChain.ThenFunc(s.handleAPIConfigPatchGroup))
	mux.Handle("POST /api/config/reload", apiChain.ThenFunc(s.handleAPIConfigReload))

	// Hardware — resources, storage, performance, GPU power.
	mux.Handle("GET "+api.RouteHardware, apiChain.ThenFunc(s.handleAPIHardware))
	mux.Handle("GET "+api.RouteHardwareStorage, apiChain.ThenFunc(s.handleAPIHardwareStorage))
	mux.Handle("GET "+api.RouteHardwarePerformance, apiChain.ThenFunc(s.handleAPIHardwarePerformance))
	mux.Handle("GET "+api.RouteHardwarePower, apiChain.ThenFunc(s.handleAPIHardwarePower))
	mux.Handle("PUT "+api.RouteHardwarePower, apiChain.ThenFunc(s.handleAPIHardwarePowerSet))
	mux.Handle("DELETE "+api.RouteHardwarePower, apiChain.ThenFunc(s.handleAPIHardwarePowerRestore))

	// System — version, capabilities, events, metrics, upgrade.
	mux.Handle("GET "+api.RouteSystemVersion, apiChain.ThenFunc(s.handleAPISystemVersion))
	mux.Handle("GET "+api.RouteSystemCapabilities, apiChain.ThenFunc(s.handleAPISystemCapabilities))
	mux.Handle("GET "+api.RouteSystemEvents, apiChain.ThenFunc(s.handleAPISystemEvents))
	mux.Handle("GET "+api.RouteSystemMetrics, apiChain.ThenFunc(s.handleAPISystemMetrics))
	mux.Handle("GET "+api.RouteSystemCaptures, apiChain.ThenFunc(s.handleAPISystemCaptures))
	mux.Handle("POST "+api.RouteSystemUpgrade, apiChain.ThenFunc(s.handleAPISystemUpgrade))
	mux.Handle("GET "+api.RouteSystemProvider, apiChain.ThenFunc(s.handleAPISystemProvider))

	s.mux = mux
	s.handler = chain.New(CreateRequestLogMiddleware(s.proxylog), CreateCORSMiddleware()).Then(mux)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// CloseStreams cancels long-lived response streams (Server-Sent Events) so a
// graceful httpServer.Shutdown can drain without blocking on them. It does not
// tear down routers; call Shutdown for that. Safe to call repeatedly.
func (s *Server) CloseStreams() {
	s.shutdownFn()
}

// Shutdown stops the local and peer routers in parallel. It is idempotent;
// repeated calls return nil without re-running shutdown.
//
// Callers must drain inflight HTTP requests (httpServer.Shutdown) before
// calling this, otherwise inflight requests 502 when their processes are torn
// down. Call CloseStreams before httpServer.Shutdown so SSE streams do not
// block the drain.
func (s *Server) Shutdown(timeout time.Duration) error {
	if !s.shuttingDown.CompareAndSwap(false, true) {
		return nil
	}
	s.shutdownFn()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, rt := range []router.Router{s.local, s.peer} {
		if rt == nil {
			continue
		}
		wg.Add(1)
		go func(rt router.Router) {
			defer wg.Done()
			if err := rt.Shutdown(timeout); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(rt)
	}

	wg.Wait()
	return errors.Join(errs...)
}
