package auth

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

const (
	maxLimiterEntries = 10000          // cap to prevent memory exhaustion
	evictionInterval  = 5 * time.Minute // evict idle entries
)

// RateLimiter provides per-account rate limiting with LRU eviction.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[uuid.UUID]*accountLimiter
	config   RateLimitConfig
}

type RateLimitConfig struct {
	MutatePerMinute  int // default 60
	ReadPerMinute    int // default 300
	MetricsPerMinute int // default 120
}

func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		MutatePerMinute:  60,
		ReadPerMinute:    300,
		MetricsPerMinute: 120,
	}
}

type accountLimiter struct {
	mutate   *rate.Limiter
	read     *rate.Limiter
	metrics  *rate.Limiter
	lastSeen time.Time
}

func NewRateLimiter(config RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[uuid.UUID]*accountLimiter),
		config:   config,
	}
	go rl.evictLoop()
	return rl
}

func (rl *RateLimiter) getLimiter(accountID uuid.UUID) *accountLimiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	l, ok := rl.limiters[accountID]
	if ok {
		l.lastSeen = time.Now()
		return l
	}

	// Evict oldest if at capacity
	if len(rl.limiters) >= maxLimiterEntries {
		rl.evictOldest()
	}

	l = &accountLimiter{
		mutate:   rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.config.MutatePerMinute)), rl.config.MutatePerMinute),
		read:     rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.config.ReadPerMinute)), rl.config.ReadPerMinute),
		metrics:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.config.MetricsPerMinute)), rl.config.MetricsPerMinute),
		lastSeen: time.Now(),
	}
	rl.limiters[accountID] = l
	return l
}

// evictOldest removes the entry with the oldest lastSeen time. Caller must hold rl.mu.
func (rl *RateLimiter) evictOldest() {
	var oldestID uuid.UUID
	var oldestTime time.Time
	first := true
	for id, l := range rl.limiters {
		if first || l.lastSeen.Before(oldestTime) {
			oldestID = id
			oldestTime = l.lastSeen
			first = false
		}
	}
	if !first {
		delete(rl.limiters, oldestID)
	}
}

// evictLoop periodically removes idle entries.
func (rl *RateLimiter) evictLoop() {
	ticker := time.NewTicker(evictionInterval)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-evictionInterval)
		for id, l := range rl.limiters {
			if l.lastSeen.Before(cutoff) {
				delete(rl.limiters, id)
			}
		}
		rl.mu.Unlock()
	}
}

// AllowMutate checks if a mutating operation is allowed.
func (rl *RateLimiter) AllowMutate(accountID uuid.UUID) bool {
	return rl.getLimiter(accountID).mutate.Allow()
}

// AllowRead checks if a read operation is allowed.
func (rl *RateLimiter) AllowRead(accountID uuid.UUID) bool {
	return rl.getLimiter(accountID).read.Allow()
}

// AllowMetrics checks if a metrics query is allowed.
func (rl *RateLimiter) AllowMetrics(accountID uuid.UUID) bool {
	return rl.getLimiter(accountID).metrics.Allow()
}
