package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// warmupRequest is the JSON body for POST /api/skein/warmup.
type warmupRequest struct {
	Model     string `json:"model"`
	Role      string `json:"role"`
	MinContext int   `json:"min_context"`
}

// warmupResponse is the JSON response for POST /api/skein/warmup.
type warmupResponse struct {
	Model      string `json:"model"`
	State      string `json:"state"`
	Warmed     bool   `json:"warmed"`
	DurationMs int    `json:"duration_ms"`
}

// apiSkeinWarmup implements POST /api/skein/warmup.
//
// Loads the specified model if not already loaded, then sends a minimal
// prefill request to prime the KV cache. Returns when loading is complete.
// Skein calls this before starting a coder session so the model is ready.
func (pm *ProxyManager) apiSkeinWarmup(c *gin.Context) {
	var req warmupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	start := time.Now()
	realModelName, found := pm.config.RealModelName(req.Model)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}

	// Check if model is already loaded and ready.
	state, loaded := pm.modelProcessState(realModelName)
	if loaded && state == "ready" {
		// Model is ready; just do a warmup prefill.
		err := pm.warmupPrefill(c.Request.Context(), realModelName)
		dur := time.Since(start).Milliseconds()
		c.JSON(http.StatusOK, warmupResponse{
			Model:      realModelName,
			State:      state,
			Warmed:     err == nil,
			DurationMs: int(dur),
		})
		return
	}

	// Model is not loaded; load it first.
	// Use the same logic as apiLoadSingleModelHandler.
	body := fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`,
		realModelName,
	)
	req2, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build load request"})
		return
	}
	req2.Header.Set("Content-Type", "application/json")

	dw := &DiscardWriter{}
	var loadErr error
	if pm.matrix != nil {
		loadErr = pm.matrix.ProxyRequest(realModelName, dw, req2)
	} else {
		processGroup, swapErr := pm.swapProcessGroup(realModelName)
		if swapErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to swap process group"})
			return
		}
		if diskErr := checkDiskSpaceForModel(pm.config.Models[realModelName].Cmd); diskErr != nil {
			c.JSON(http.StatusInsufficientStorage, gin.H{"error": diskErr.Error()})
			return
		}
		loadErr = processGroup.ProxyRequest(realModelName, dw, req2)
	}
	if loadErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model"})
		return
	}

	// Now do a warmup prefill if min_context is specified.
	if req.MinContext > 0 {
		_ = pm.warmupPrefill(c.Request.Context(), realModelName)
	}

	state, loaded = pm.modelProcessState(realModelName)
	dur := time.Since(start).Milliseconds()
	c.JSON(http.StatusOK, warmupResponse{
		Model:      realModelName,
		State:      state,
		Warmed:     loaded && state == "ready",
		DurationMs: int(dur),
	})
}

// warmupPrefill sends a minimal completion request to prime the KV cache.
func (pm *ProxyManager) warmupPrefill(ctx context.Context, model string) error {
	body := fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":2,"stream":false}`,
		model,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	dw := &DiscardWriter{}
	if pm.matrix != nil {
		return pm.matrix.ProxyRequest(model, dw, req)
	}
	processGroup := pm.findGroupByModelName(model)
	if processGroup == nil {
		return fmt.Errorf("process group not found for model %s", model)
	}
	return processGroup.ProxyRequest(model, dw, req)
}
