package proxy

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Reservation represents an active model reservation that prevents llama-swap
// from evicting a model mid-session.
type Reservation struct {
	ID        string    `json:"reservation_id"`
	Model     string    `json:"model"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// reserveRequest is the JSON body for POST /api/skein/reserve.
type reserveRequest struct {
	Model        string `json:"model"`
	Role         string `json:"role"`
	DurationSecs int    `json:"duration_secs"`
}

// reserveResponse is the JSON response for POST /api/skein/reserve.
type reserveResponse struct {
	ID        string    `json:"reservation_id"`
	Model     string    `json:"model"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ReservationStore holds active reservations with a cleanup goroutine.
type ReservationStore struct {
	mu   sync.RWMutex
	reservations map[string]*Reservation // keyed by ID
}

// NewReservationStore creates a reservation store and starts the expiry cleanup goroutine.
func NewReservationStore(ctx context.Context) *ReservationStore {
	s := &ReservationStore{
		reservations: make(map[string]*Reservation),
	}
	go s.cleanupLoop(ctx)
	return s
}

// Add creates a new reservation and returns its ID.
func (s *ReservationStore) Add(model, role string, durationSecs int) *Reservation {
	if durationSecs <= 0 {
		durationSecs = 1800 // 30 min default
	}
	now := time.Now()
	r := &Reservation{
		ID:        "rsv_" + uuid.New().String()[:12],
		Model:     model,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(durationSecs) * time.Second),
	}
	s.mu.Lock()
	s.reservations[r.ID] = r
	s.mu.Unlock()
	return r
}

// Get returns a reservation by ID, or nil if not found/expired.
func (s *ReservationStore) Get(id string) *Reservation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.reservations[id]
	if !ok || time.Now().After(r.ExpiresAt) {
		return nil
	}
	return r
}

// Delete removes a reservation by ID.
func (s *ReservationStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.reservations[id]
	if ok {
		delete(s.reservations, id)
	}
	return ok
}

// HasActiveFor returns true if any active reservation exists for the given model.
func (s *ReservationStore) HasActiveFor(model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for _, r := range s.reservations {
		if r.Model == model && now.Before(r.ExpiresAt) {
			return true
		}
	}
	return false
}

// cleanupLoop runs periodically to remove expired reservations.
func (s *ReservationStore) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.removeExpired()
		}
	}
}

func (s *ReservationStore) removeExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, r := range s.reservations {
		if now.After(r.ExpiresAt) {
			delete(s.reservations, id)
		}
	}
}

// apiSkeinReserve implements POST /api/skein/reserve.
// Creates a reservation that prevents llama-swap from evicting the model.
func (pm *ProxyManager) apiSkeinReserve(c *gin.Context) {
	var req reserveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	r := pm.reservationStore.Add(req.Model, req.Role, req.DurationSecs)
	c.JSON(http.StatusCreated, reserveResponse{
		ID:        r.ID,
		Model:     r.Model,
		ExpiresAt: r.ExpiresAt,
	})
}

// apiSkeinReleaseReservation implements DELETE /api/skein/reserve/{id}.
func (pm *ProxyManager) apiSkeinReleaseReservation(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reservation ID is required"})
		return
	}
	if !pm.reservationStore.Delete(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "reservation not found or expired"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"reservation_id": id, "released": true})
}
