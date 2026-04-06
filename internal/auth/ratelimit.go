package auth

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// RateLimiter provides per-account rate limiting.
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
	mutate  *rate.Limiter
	read    *rate.Limiter
	metrics *rate.Limiter
}

func NewRateLimiter(config RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[uuid.UUID]*accountLimiter),
		config:   config,
	}
}

func (rl *RateLimiter) getLimiter(accountID uuid.UUID) *accountLimiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	l, ok := rl.limiters[accountID]
	if !ok {
		l = &accountLimiter{
			mutate:  rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.config.MutatePerMinute)), rl.config.MutatePerMinute),
			read:    rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.config.ReadPerMinute)), rl.config.ReadPerMinute),
			metrics: rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.config.MetricsPerMinute)), rl.config.MetricsPerMinute),
		}
		rl.limiters[accountID] = l
	}
	return l
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
