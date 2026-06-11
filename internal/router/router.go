package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/logmon"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/tidwall/gjson"
)

type contextkey struct {
	name string
}

type ReqContextData struct {
	Model            string
	ModelID          string
	Streaming        bool
	SendLoadingState bool
	LoadingTheme     LoadingTheme

	// ModelDefaulted is true when the request did not name a model and the
	// configured defaultModel was substituted. The filter middleware uses it
	// to inject the model into the forwarded body, since some backends
	// (vLLM, MLX) reject requests without a model field.
	ModelDefaulted bool
}

var (
	// ErrModelMissing reports that the request named no model at all (as
	// opposed to an unparseable body). FetchContext substitutes the
	// configured defaultModel for this error only.
	ErrModelMissing = fmt.Errorf("no model specified in request")

	ErrNoModelInContext  = fmt.Errorf("no model in request context")
	ErrNoRouterFound     = fmt.Errorf("no router found for model")
	ErrNoPeerModelFound  = fmt.Errorf("peer model not found")
	ErrNoLocalModelFound = fmt.Errorf("local model not found")

	ContextKey = &contextkey{"context"}
)

type Router interface {
	// Shutdown blocks until the router has shutdown returning nil
	// when the router has shutdown successfully.
	//
	// timeout controls how long to wait for inflight requests to finish. After
	// the timeout all inflight requests will be cancelled.
	Shutdown(timeout time.Duration) error

	// ServeHTTP implements the http.Handler and requests coming in will
	// trigger any model swapping and routing logic.
	ServeHTTP(http.ResponseWriter, *http.Request)

	// Handles reports whether this router can serve requests for the given model.
	Handles(model string) bool
}

// LocalRouter is a Router backed by local processes whose state can be
// inspected and which can be individually stopped. Peer routers, which only
// forward to remote hosts, do not implement it.
type LocalRouter interface {
	Router

	// RunningModels returns the current state of every process that is not
	// stopped or shut down, keyed by model ID.
	RunningModels() map[string]process.ProcessState

	// Unload stops the named models, or every running model when none are
	// named. It blocks until each targeted process has stopped.
	Unload(timeout time.Duration, models ...string)

	// ProcessLogger returns the log monitor for the named model's process.
	// modelID must be a real (non-alias) config key. Returns false when the
	// model is not known to this router.
	ProcessLogger(modelID string) (*logmon.Monitor, bool)
}

// FetchContext will attempt to get the model id from the context then
// from the model body. If it extracts the model from the body it will
// store the model in the context for downstream handlers. An error
// will be returned when model can not be fetch from either location.
func FetchContext(r *http.Request, cfg config.Config) (ReqContextData, error) {
	data, ok := ReadContext(r.Context())
	if ok {
		return data, nil
	}

	data, err := ExtractContext(r)
	if err != nil {
		// fall back to the configured default model only when the request
		// simply named no model; parse failures still error out.
		if !errors.Is(err, ErrModelMissing) || cfg.DefaultModel == "" {
			return ReqContextData{}, ErrNoModelInContext
		}
		data.Model = cfg.DefaultModel
		data.ModelDefaulted = true
	}

	realName, _ := cfg.RealModelName(data.Model)
	if realName == "" {
		realName = data.Model
	}
	data.ModelID = realName
	if mc, ok := cfg.Models[realName]; ok {
		data.SendLoadingState = mc.SendLoadingState != nil && *mc.SendLoadingState
	}
	data.LoadingTheme = LoadingTheme(r.Header.Get("X-Loading-Theme"))
	*r = *r.WithContext(SetContext(r.Context(), data))
	return data, nil
}

func SetContext(ctx context.Context, data ReqContextData) context.Context {
	return context.WithValue(ctx, ContextKey, data)
}

func ReadContext(ctx context.Context) (ReqContextData, bool) {
	data, ok := ctx.Value(ContextKey).(ReqContextData)
	return data, ok
}

// ExtractContext pulls the model name from an HTTP request without consuming the
// body. For GET requests it reads the "model" query parameter. For POST
// requests it inspects Content-Type and parses JSON, multipart/form-data, or
// application/x-www-form-urlencoded bodies. The request body is always restored
// before returning so downstream handlers — including reverse proxies that
// forward raw bytes upstream — can still read it.
func ExtractContext(r *http.Request) (ReqContextData, error) {
	if r.Method == http.MethodGet {
		if model := r.URL.Query().Get("model"); model != "" {
			return ReqContextData{Model: model, Streaming: r.URL.Query().Get("stream") == "true"}, nil
		}
		return ReqContextData{Streaming: r.URL.Query().Get("stream") == "true"},
			fmt.Errorf("missing 'model' query parameter: %w", ErrModelMissing)
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return ReqContextData{}, fmt.Errorf("error reading request body: %w", err)
	}
	defer func() {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}()

	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/json") {
		model := gjson.GetBytes(bodyBytes, "model").String()
		if model == "" {
			return ReqContextData{Streaming: gjson.GetBytes(bodyBytes, "stream").Bool()},
				fmt.Errorf("missing or empty 'model' in JSON body: %w", ErrModelMissing)
		}
		return ReqContextData{Model: model, Streaming: gjson.GetBytes(bodyBytes, "stream").Bool()}, nil
	}

	// Form parsers read from r.Body, so feed them a fresh reader over the
	// buffered bytes. The deferred restore above will reset r.Body again
	// after parsing.
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return ReqContextData{}, fmt.Errorf("error parsing multipart form: %w", err)
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return ReqContextData{}, fmt.Errorf("error parsing form: %w", err)
		}
	}

	if model := r.FormValue("model"); model != "" {
		return ReqContextData{Model: model, Streaming: r.FormValue("stream") == "true"}, nil
	}

	return ReqContextData{Streaming: r.FormValue("stream") == "true"},
		fmt.Errorf("missing 'model' parameter: %w", ErrModelMissing)
}

func SendError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNoModelInContext):
		SendResponse(w, r, http.StatusNotFound, "no model id could be identified")
	case errors.Is(err, ErrNoPeerModelFound):
		SendResponse(w, r, http.StatusNotFound, "no peer found for requested model")
	case errors.Is(err, ErrNoLocalModelFound):
		SendResponse(w, r, http.StatusNotFound, "no local server found for requested model")
	case errors.Is(err, ErrNoRouterFound):
		SendResponse(w, r, http.StatusNotFound, "no router for requested model")
	default:
		SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("unspecific error: %v", err))
	}
}

// SendResponse detects what content type the client prefers and returns an error response in that format.
func SendResponse(w http.ResponseWriter, r *http.Request, status int, message string) {
	// Check Accept header for preferred response format
	acceptHeader := r.Header.Get("Accept")
	if strings.Contains(acceptHeader, "text/plain") {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(status)
		w.Write([]byte(fmt.Sprintf("llama-skein: %s", message)))
		return
	}

	if strings.Contains(acceptHeader, "text/html") {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		w.Write([]byte(fmt.Sprintf(`<html><body><h1>llama-skein</h1><p>%s</p></body></html>`, message)))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(fmt.Sprintf(`{"src":"llama-skein", "error": "%s"}`, message)))
}
