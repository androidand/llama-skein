package proxy

import (
	"net/http"

	"github.com/mostlygeek/llama-swap/internal/thermal"
	"github.com/gin-gonic/gin"
)

// apiSkeinSilentGet implements GET /api/skein/silent.
func (pm *ProxyManager) apiSkeinSilentGet(c *gin.Context) {
	c.JSON(http.StatusOK, pm.silentMode.GetState())
}

// apiSkeinSilentEnable implements POST /api/skein/silent.
// Body is optional: { "power_limit_pct": 65, "temp_target_celsius": 82 }
// Omitting body uses DefaultSilentProfile.
func (pm *ProxyManager) apiSkeinSilentEnable(c *gin.Context) {
	profile := thermal.DefaultSilentProfile
	// Allow caller to override individual fields.
	_ = c.ShouldBindJSON(&profile)

	if err := pm.silentMode.Apply(profile); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, pm.silentMode.GetState())
}

// apiSkeinSilentDisable implements DELETE /api/skein/silent.
func (pm *ProxyManager) apiSkeinSilentDisable(c *gin.Context) {
	if err := pm.silentMode.Restore(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, pm.silentMode.GetState())
}
