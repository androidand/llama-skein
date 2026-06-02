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
	"loading-themes",
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
