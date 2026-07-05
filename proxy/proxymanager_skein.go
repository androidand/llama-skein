package proxy

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// SkeinCapabilitiesVersion is the current version of the Skein companion API.
const SkeinCapabilitiesVersion = 1

// SkeinCapabilities represents the response from GET /api/skein/capabilities.
type SkeinCapabilities struct {
	Version  int      `json:"version"`
	Features []string `json:"features"`
}

// currentSkeinFeatures lists all Skein companion features this version supports.
var currentSkeinFeatures = []string{
	"capabilities",
	"silent-mode",
	"reserve",
	"warmup",
	"session-metrics",
	"role-routing",
}

// addSkeinHandlers registers all /api/skein/* companion endpoints.
//
// These routes extend llama-swap with Skein-specific operations beyond the
// OpenAI-compatible surface. A stock llama-swap (without this code) returns
// 404 for all /api/skein/ routes; Skein detects this and falls back to plain
// OpenAI mode.
func addSkeinHandlers(pm *ProxyManager) {
	skeinGroup := pm.ginEngine.Group("/api/skein", pm.apiKeyAuth())
	{
		skeinGroup.GET("/capabilities", pm.apiSkeinCapabilities)
		skeinGroup.GET("/silent", pm.apiSkeinSilentGet)
		skeinGroup.POST("/silent", pm.apiSkeinSilentEnable)
		skeinGroup.DELETE("/silent", pm.apiSkeinSilentDisable)
		// Reservation endpoints
		skeinGroup.POST("/reserve", pm.apiSkeinReserve)
		skeinGroup.DELETE("/reserve/:id", pm.apiSkeinReleaseReservation)
		// Warmup endpoint
		skeinGroup.POST("/warmup", pm.apiSkeinWarmup)
		// Session metrics endpoint
		skeinGroup.GET("/metrics/session/:reservation_id", pm.apiSkeinSessionMetrics)
	}
}

// apiSkeinCapabilities implements GET /api/skein/capabilities.
//
// Returns the list of companion features supported by this instance.
// Skein calls this on first connection to discover what is available.
// If the endpoint returns 404, Skein treats the backend as a plain
// OpenAI-compatible server.
func (pm *ProxyManager) apiSkeinCapabilities(c *gin.Context) {
	c.JSON(http.StatusOK, SkeinCapabilities{
		Version:  SkeinCapabilitiesVersion,
		Features: currentSkeinFeatures,
	})
}

// resolveSkeinRoleRouting checks X-Skein-Role and X-Skein-Tier headers when
// the request body does not specify a model. If a loaded model's metadata
// matches both role and tier, returns its model ID. Otherwise returns empty.
//
// Priority: explicit model name in body > reservation > role+tier hint > first loaded.
func (pm *ProxyManager) resolveSkeinRoleRouting(c *gin.Context) string {
	role := c.GetHeader("X-Skein-Role")
	tier := c.GetHeader("X-Skein-Tier")
	if role == "" && tier == "" {
		return ""
	}

	for modelID, cfg := range pm.config.Models {
		// Check if model is currently loaded/ready
		state, loaded := pm.modelProcessState(modelID)
		if !loaded || state != string(StateReady) {
			continue
		}

		meta := cfg.Metadata
		if meta == nil {
			continue
		}

		modelRole, _ := meta["role"].(string)
		modelTier, _ := meta["tier"].(string)

		// Match role if provided, tier if provided
		if role != "" && modelRole != role {
			continue
		}
		if tier != "" && modelTier != tier {
			continue
		}

		pm.proxyLogger.Infof("Skein role routing: model=%s matched role=%q tier=%q", modelID, role, tier)
		return modelID
	}

	// Fallback: if only role or tier was provided and no match, return first loaded model
	if role != "" || tier != "" {
		for modelID := range pm.config.Models {
			state, loaded := pm.modelProcessState(modelID)
			if loaded && state == string(StateReady) {
				pm.proxyLogger.Infof("Skein role routing: no exact match for role=%q tier=%q, falling back to first loaded model=%s", role, tier, modelID)
				return modelID
			}
		}
	}

	return ""
}
