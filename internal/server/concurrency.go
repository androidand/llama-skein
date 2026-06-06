package server

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/androidand/llama-skein/internal/chain"
	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/router"
)

// defaultConcurrencyLimit caps simultaneous in-flight requests per model when
// the model config leaves concurrencyLimit unset. Matches the legacy
// proxy.Process default.
const defaultConcurrencyLimit = 10

// acquireTimeout is the maximum time a request will wait in the queue for a
// concurrency slot. 30s is enough to survive model loading; if the slot isn't
// available by then the request is rejected.
const acquireTimeout = 30 * time.Second

// CreateConcurrencyMiddleware returns middleware that limits simultaneous
// model-dispatched requests per model. Each model gets a semaphore sized to
// its concurrencyLimit (or defaultConcurrencyLimit). A request that cannot
// immediately acquire a slot will wait up to acquireTimeout, then is rejected
// with 429 if the slot is still unavailable. Models without a local config
// entry (e.g. peer-routed models) are not limited.
func CreateConcurrencyMiddleware(cfg config.Config) chain.Middleware {
	semaphores := make(map[string]*semaphore.Weighted, len(cfg.Models))
	for id, mc := range cfg.Models {
		limit := defaultConcurrencyLimit
		if mc.ConcurrencyLimit > 0 {
			limit = mc.ConcurrencyLimit
		}
		semaphores[id] = semaphore.NewWeighted(int64(limit))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := router.FetchContext(r, cfg)
			if err != nil {
				router.SendError(w, r, router.ErrNoModelInContext)
				return
			}

			// fall through for peer models
			sem, ok := semaphores[data.ModelID]
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// Block with timeout instead of immediately rejecting.
			// This allows concurrent discovery requests to queue up rather
			// than 429 each other during startup.
			ctx, cancel := context.WithTimeout(r.Context(), acquireTimeout)
			defer cancel()
			if err := sem.Acquire(ctx, 1); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":"Too many requests"}`))
				return
			}
			defer sem.Release(1)
			next.ServeHTTP(w, r)
		})
	}
}
