package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyManager_SkeinCapabilities_AdvertisesLoadingThemes(t *testing.T) {
	cfg := testConfigFromYAML(t, `
logLevel: error
models:
  test-model:
    cmd: {{RESPONDER}} --port ${PORT} --silent --respond "hello"
`)
	p := New(cfg)

	req := httptest.NewRequest("GET", "/api/skein/capabilities", nil)
	w := CreateTestResponseRecorder()

	p.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Version  int      `json:"version"`
		Features []string `json:"features"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	assert.Equal(t, 1, resp.Version)
	assert.Contains(t, resp.Features, "loading-themes")
}
