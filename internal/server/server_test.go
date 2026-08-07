package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/event"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/internal/router"
	"github.com/androidand/llama-skein/internal/shared"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

// stubRouter is a minimal router.LocalRouter for Server dispatch tests.
type stubRouter struct {
	models        map[string]bool
	response      string
	shutdownCalls atomic.Int32
	running       map[string]process.ProcessState
	unloadCalls   atomic.Int32
	loggers       map[string]*logmon.Monitor
	modelErrors   map[string]*process.LoadError
	// serveStatus overrides ServeHTTP's response status; zero means 200.
	// Added for task 5.2's tests, which need to simulate a warm/load
	// request that fails outright — every other field predates that task.
	serveStatus int
	// cmdOverrides records adaptive-placement retry commands per model.
	cmdOverrides map[string]string
	// resident maps model id -> resident bytes reported by the fake.
	resident map[string]int64
}

func newStubRouter(models []string, response string) *stubRouter {
	m := make(map[string]bool, len(models))
	for _, id := range models {
		m[id] = true
	}
	return &stubRouter{models: m, response: response}
}

func (s *stubRouter) Handles(model string) bool      { return s.models[model] }
func (s *stubRouter) Shutdown(_ time.Duration) error { s.shutdownCalls.Add(1); return nil }
func (s *stubRouter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	status := s.serveStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	w.Write([]byte(s.response))
}

func (s *stubRouter) RunningModels() map[string]process.ProcessState { return s.running }

func (s *stubRouter) ModelErrors() map[string]*process.LoadError { return s.modelErrors }
func (s *stubRouter) Unload(_ time.Duration, _ ...string)        { s.unloadCalls.Add(1) }
func (s *stubRouter) SetCommandOverride(modelID, cmd string, _ int) bool {
	if s.cmdOverrides == nil {
		s.cmdOverrides = map[string]string{}
	}
	s.cmdOverrides[modelID] = cmd
	return true
}

func (s *stubRouter) ResidentBytes(modelID string) int64 { return s.resident[modelID] }

func (s *stubRouter) ProcessLogger(modelID string) (*logmon.Monitor, bool) {
	if s.loggers != nil {
		if lg, ok := s.loggers[modelID]; ok {
			return lg, true
		}
	}
	return nil, false
}

// newTestServer wires a Server with stub routers and a built mux.
func newTestServer(local router.LocalRouter, peer router.Router) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	proxylog := logmon.NewWriter(io.Discard)
	s := &Server{
		cfg:         config.Config{},
		muxlog:      logmon.NewWriter(io.Discard),
		proxylog:    proxylog,
		upstreamlog: logmon.NewWriter(io.Discard),
		inflight:    &inflightCounter{},
		metrics:     newMetricsMonitor(proxylog, 0, 0),
		local:       local,
		peer:        peer,
		shutdownCtx: ctx,
		shutdownFn:  cancel,
	}
	s.routes()
	return s
}

func chatRequest(model string) *http.Request {
	body := strings.NewReader(`{"model":"` + model + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestServer_New_GroupConfig(t *testing.T) {
	discard := logmon.NewWriter(io.Discard)
	s, err := New(config.Config{HealthCheckTimeout: 15}, discard, discard, discard, nil, BuildInfo{})
	if err != nil {
		t.Fatalf("New (group): %v", err)
	}
	if err := s.Shutdown(time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestServer_New_InitializesOperationStoreAndRecovers exercises New()'s real
// wiring (operation.DefaultStateDir + operation.Recover), not a mock — same
// level of real-home-directory touch this package already accepts for
// profileStore in TestServer_New_GroupConfig above. A failure to initialize
// the operation store must not fail New() itself (see the storeErr handling
// in New): model management degrades, inference still starts.
func TestServer_New_InitializesOperationStoreAndRecovers(t *testing.T) {
	discard := logmon.NewWriter(io.Discard)
	s, err := New(config.Config{HealthCheckTimeout: 15}, discard, discard, discard, nil, BuildInfo{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Shutdown(time.Second)
	if s.operationStore == nil {
		t.Fatal("operationStore is nil after New() — recovery has nothing to run against")
	}
}

func TestServer_New_MatrixConfig(t *testing.T) {
	discard := logmon.NewWriter(io.Discard)
	cfg := config.Config{HealthCheckTimeout: 15, Matrix: &config.MatrixConfig{}}
	s, err := New(cfg, discard, discard, discard, nil, BuildInfo{})
	if err != nil {
		t.Fatalf("New (matrix): %v", err)
	}
	if err := s.Shutdown(time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestServer_RouteToLocalModel(t *testing.T) {
	s := newTestServer(
		newStubRouter([]string{"local-model"}, "local response"),
		newStubRouter(nil, ""),
	)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, chatRequest("local-model"))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if w.Body.String() != "local response" {
		t.Errorf("body=%q want %q", w.Body.String(), "local response")
	}
}

func TestServer_RouteToPeerModel(t *testing.T) {
	s := newTestServer(
		newStubRouter(nil, ""),
		newStubRouter([]string{"peer-model"}, "peer response"),
	)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, chatRequest("peer-model"))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if w.Body.String() != "peer response" {
		t.Errorf("body=%q want %q", w.Body.String(), "peer response")
	}
}

func TestServer_UnknownModelReturns404(t *testing.T) {
	s := newTestServer(
		newStubRouter([]string{"local-model"}, ""),
		newStubRouter(nil, ""),
	)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, chatRequest("unknown-model"))

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%q", w.Code, w.Body.String())
	}
}

func TestServer_UnknownPathReturns404(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", w.Code)
	}
}

// TestServer_WolHealth pins /wol-health as a bare constant: wake-on-LAN probes
// only need to know the host answers, and some do not parse a body.
func TestServer_WolHealth(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/wol-health", nil))
	if w.Code != http.StatusOK || w.Body.String() != "OK" {
		t.Errorf("status=%d body=%q", w.Code, w.Body.String())
	}
}

// TestServer_Health covers the readiness body. /health used to be a hardcoded
// "OK" that said nothing about any model, so a provider whose model had failed
// to load looked exactly like a healthy one and callers dispatched into it.
func TestServer_Health(t *testing.T) {
	local := newStubRouter([]string{"good", "broken"}, "")
	local.running = map[string]process.ProcessState{
		"good":   process.StateReady,
		"broken": process.StateFailed,
	}
	local.modelErrors = map[string]*process.LoadError{
		"broken": {Message: "exec: no such file", Category: process.FailureStart, Attempts: 2},
	}
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Models: map[string]config.ModelConfig{
		"good": {Cmd: "llama-server"}, "broken": {Cmd: "llama-server"},
	}}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}

	var got struct {
		Status           string `json:"status"`
		AnyModelResident bool   `json:"any_model_resident"`
		Busy             bool   `json:"busy"`
		Models           map[string]struct {
			State     string `json:"state"`
			LastError *struct {
				Message  string `json:"message"`
				Category string `json:"category"`
				Attempts int    `json:"attempts"`
			} `json:"last_error"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%q", err, w.Body.String())
	}

	if got.Status != "ok" {
		t.Errorf("status=%q want ok", got.Status)
	}
	if !got.AnyModelResident {
		t.Error("any_model_resident=false with a ready model")
	}
	if got.Busy {
		t.Error("busy=true with nothing in flight")
	}
	if got.Models["good"].State != "ready" {
		t.Errorf("good.state=%q want ready", got.Models["good"].State)
	}
	// The point of the change: a broken model is distinguishable from an idle
	// one, and says why.
	if got.Models["broken"].State != "failed" {
		t.Errorf("broken.state=%q want failed", got.Models["broken"].State)
	}
	le := got.Models["broken"].LastError
	if le == nil {
		t.Fatal("broken model reported no last_error")
	}
	if le.Message != "exec: no such file" || le.Category != "start" || le.Attempts != 2 {
		t.Errorf("last_error=%+v", *le)
	}
	if got.Models["good"].LastError != nil {
		t.Error("healthy model carries a last_error")
	}
}

// TestServer_HealthNoModelResident covers the reachable-but-not-serving case:
// the control plane answers while nothing is loaded, which a bare "OK" could
// never express.
func TestServer_HealthNoModelResident(t *testing.T) {
	local := newStubRouter([]string{"m"}, "")
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Models: map[string]config.ModelConfig{"m": {Cmd: "llama-server"}}}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	var got struct {
		AnyModelResident bool `json:"any_model_resident"`
		Models           map[string]struct {
			State string `json:"state"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AnyModelResident {
		t.Error("any_model_resident=true with nothing loaded")
	}
	if got.Models["m"].State != "stopped" {
		t.Errorf("state=%q want stopped", got.Models["m"].State)
	}
}

func TestServer_CORSPreflight(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin=%q want *", got)
	}
}

func TestServer_Unload(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "")
	s := newTestServer(local, newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/unload", nil))

	if w.Code != http.StatusOK || w.Body.String() != "OK" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if got := local.unloadCalls.Load(); got != 1 {
		t.Errorf("unloadCalls=%d want 1", got)
	}
}

func TestServer_Running(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "")
	local.running = map[string]process.ProcessState{"m1": process.StateReady}
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Models: map[string]config.ModelConfig{
		"m1": {
			Cmd:         "llama-server",
			Proxy:       "http://localhost:9999",
			UnloadAfter: 300,
			Name:        "Model One",
			Description: "the first model",
		},
	}}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/running", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}

	var resp struct {
		Running []runningModel `json:"running"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%q", err, w.Body.String())
	}
	if len(resp.Running) != 1 {
		t.Fatalf("running=%v want 1 entry", resp.Running)
	}
	want := runningModel{
		Model:       "m1",
		State:       "ready",
		Cmd:         "llama-server",
		Proxy:       "http://localhost:9999",
		TTL:         300,
		Name:        "Model One",
		Description: "the first model",
	}
	if resp.Running[0] != want {
		t.Errorf("got %+v want %+v", resp.Running[0], want)
	}
}

func TestServer_Preload(t *testing.T) {
	local := newStubRouter([]string{"m1"}, "ok")
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Hooks: config.HooksConfig{
		OnStartup: config.HookOnStartup{Preload: []string{"m1"}},
	}}

	got := make(chan shared.ModelPreloadedEvent, 1)
	cancel := event.On(func(e shared.ModelPreloadedEvent) { got <- e })
	defer cancel()

	s.startPreload()

	select {
	case e := <-got:
		if e.ModelName != "m1" || !e.Success {
			t.Errorf("event=%+v want {ModelName:m1 Success:true}", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preload event not received")
	}
}

func TestServer_Shutdown_StopsRoutersAndIsIdempotent(t *testing.T) {
	local := newStubRouter([]string{"local-model"}, "")
	peer := newStubRouter(nil, "")
	s := newTestServer(local, peer)

	if err := s.Shutdown(time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := s.Shutdown(time.Second); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if got := local.shutdownCalls.Load(); got != 1 {
		t.Errorf("local shutdownCalls=%d want 1", got)
	}
	if got := peer.shutdownCalls.Load(); got != 1 {
		t.Errorf("peer shutdownCalls=%d want 1", got)
	}
}

func TestServer_LogStream_ModelID(t *testing.T) {
	buf := logmon.NewWriter(io.Discard)
	buf.Write([]byte("hello from model"))

	local := newStubRouter([]string{"mymodel"}, "")
	local.loggers = map[string]*logmon.Monitor{"mymodel": buf}

	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Models: map[string]config.ModelConfig{"mymodel": {}}}

	// Pre-cancel the context so the streaming loop exits immediately after
	// flushing history.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodGet, "/logs/stream/mymodel", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "hello from model" {
		t.Errorf("body=%q want %q", got, "hello from model")
	}
}

func TestServer_LogStream_UnknownID_Returns400(t *testing.T) {
	s := newTestServer(newStubRouter(nil, ""), newStubRouter(nil, ""))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs/stream/no-such-model", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", w.Code)
	}
}

// TestServer_HealthMatchesContract decodes the live handler response into the
// generated contract type, so the handler and contracts/llama-skein.openapi.json
// cannot drift apart silently. Without the contract entry, a cross-repo client
// generated from the spec could not read readiness at all.
func TestServer_HealthMatchesContract(t *testing.T) {
	local := newStubRouter([]string{"broken"}, "")
	local.running = map[string]process.ProcessState{"broken": process.StateFailed}
	local.modelErrors = map[string]*process.LoadError{
		"broken": {Message: "boom", Category: process.FailureStart, Attempts: 1},
	}
	s := newTestServer(local, newStubRouter(nil, ""))
	s.cfg = config.Config{Models: map[string]config.ModelConfig{"broken": {Cmd: "llama-server"}}}

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	dec.DisallowUnknownFields()
	var got apicontract.HealthResponse
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("response does not match the contract schema: %v\nbody=%s", err, w.Body.String())
	}

	if got.Status != "ok" || !got.Busy == false {
		t.Errorf("status=%q busy=%v", got.Status, got.Busy)
	}
	if got.AnyModelResident {
		t.Error("any_model_resident=true with only a failed model")
	}
	mh, ok := got.Models["broken"]
	if !ok {
		t.Fatal("failed model missing from the contract-typed response")
	}
	if string(mh.State) != "failed" {
		t.Errorf("state=%q want failed", mh.State)
	}
	if mh.LastError == nil || mh.LastError.Message != "boom" {
		t.Errorf("last_error did not survive contract decoding: %+v", mh.LastError)
	}
}
