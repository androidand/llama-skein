package proxy

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SessionMetrics accumulates per-reservation performance metrics.
type SessionMetrics struct {
	TokensPrompt     int     `json:"tokens_prompt"`
	TokensCompletion int     `json:"tokens_completion"`
	Requests         int     `json:"requests"`
	AvgTTFTMs        float64 `json:"avg_ttft_ms"`
	AvgTPS           float64 `json:"avg_tps"`
	Model            string  `json:"model"`
	DurationSecs     int     `json:"duration_secs"`
}

// SessionMetricsStore accumulates metrics per reservation ID.
// It listens to ActivityLogEvent and aggregates by the X-Skein-Reservation header.
type SessionMetricsStore struct {
	mu    sync.RWMutex
	data  map[string]*sessionMetricsAccum
}

// sessionMetricsAccum is the internal accumulator for a single reservation.
type sessionMetricsAccum struct {
	tokensPrompt     int
	tokensCompletion int
	requests         int
	totalTTFTMs      float64
	totalTPS         float64
	model            string
	startTime        time.Time
	lastTime         time.Time
}

// NewSessionMetricsStore creates a new session metrics store.
func NewSessionMetricsStore() *SessionMetricsStore {
	return &SessionMetricsStore{
		data: make(map[string]*sessionMetricsAccum),
	}
}

// Record adds an activity log entry to the accumulator for the given reservation ID.
func (s *SessionMetricsStore) Record(reservationID string, entry ActivityLogEntry) {
	if reservationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.data[reservationID]
	if !ok {
		a = &sessionMetricsAccum{
			startTime: entry.Timestamp,
			model:     entry.Model,
		}
		s.data[reservationID] = a
	}

	a.tokensPrompt += int(entry.Tokens.InputTokens)
	a.tokensCompletion += int(entry.Tokens.OutputTokens)
	a.requests++
	a.lastTime = entry.Timestamp

	// Estimate TTFT and TPS from duration_ms.
	if entry.DurationMs > 0 {
		ttft := float64(entry.DurationMs) / 2 // rough estimate: half of total is time-to-first-token
		tps := float64(a.tokensCompletion) / float64(entry.DurationMs) * 1000
		a.totalTTFTMs += ttft
		a.totalTPS += tps
	}
}

// Get returns the accumulated metrics for a reservation ID.
func (s *SessionMetricsStore) Get(reservationID string) *SessionMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data[reservationID]
	if !ok {
		return nil
	}

	durationSecs := int(time.Since(a.startTime).Seconds())
	if durationSecs <= 0 {
		durationSecs = 1
	}

	avgTTFT := 0.0
	avgTPS := 0.0
	if a.requests > 0 {
		avgTTFT = a.totalTTFTMs / float64(a.requests)
		avgTPS = a.totalTPS / float64(a.requests)
	}

	return &SessionMetrics{
		TokensPrompt:     a.tokensPrompt,
		TokensCompletion: a.tokensCompletion,
		Requests:         a.requests,
		AvgTTFTMs:        avgTTFT,
		AvgTPS:           avgTPS,
		Model:            a.model,
		DurationSecs:     durationSecs,
	}
}

// apiSkeinSessionMetrics implements GET /api/skein/metrics/session/{reservation_id}.
func (pm *ProxyManager) apiSkeinSessionMetrics(c *gin.Context) {
	reservationID := c.Param("reservation_id")
	if reservationID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reservation_id is required"})
		return
	}

	metrics := pm.sessionMetricsStore.Get(reservationID)
	if metrics == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no metrics found for reservation"})
		return
	}

	c.JSON(http.StatusOK, metrics)
}
