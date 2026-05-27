package security

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RateLimiter implements token bucket rate limiting
type RateLimiter struct {
	config RateLimiterConfig
	logger *zap.Logger

	// Token bucket state
	buckets map[string]*TokenBucket
	mu      sync.RWMutex
}

// RateLimiterConfig contains rate limiter configuration
type RateLimiterConfig struct {
	RPS    int           `json:"rps"`
	Burst  int           `json:"burst"`
	Window time.Duration `json:"window"`
	Logger *zap.Logger   `json:"-"`
}

// TokenBucket represents a token bucket for rate limiting
type TokenBucket struct {
	capacity   int
	tokens     int
	refillRate int
	lastRefill time.Time
	mu         sync.Mutex
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(config RateLimiterConfig) (*RateLimiter, error) {
	if config.RPS <= 0 {
		return nil, fmt.Errorf("RPS must be positive")
	}
	if config.Burst <= 0 {
		config.Burst = config.RPS // Default burst to RPS
	}

	rl := &RateLimiter{
		config:  config,
		logger:  config.Logger,
		buckets: make(map[string]*TokenBucket),
	}

	// Start cleanup goroutine
	go rl.cleanup()

	return rl, nil
}

// CheckLimit checks if the client is within rate limits
func (rl *RateLimiter) CheckLimit(clientID string) error {
	bucket := rl.getBucket(clientID)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	// Refill tokens based on time elapsed
	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill)
	tokensToAdd := int(elapsed.Seconds() * float64(bucket.refillRate))

	if tokensToAdd > 0 {
		bucket.tokens = min(bucket.capacity, bucket.tokens+tokensToAdd)
		bucket.lastRefill = now
	}

	// Check if we have enough tokens
	if bucket.tokens > 0 {
		bucket.tokens--
		return nil
	}

	return fmt.Errorf("rate limit exceeded for client %s", clientID)
}

// getBucket gets or creates a token bucket for the client
func (rl *RateLimiter) getBucket(clientID string) *TokenBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.buckets[clientID]
	if !exists {
		bucket = &TokenBucket{
			capacity:   rl.config.Burst,
			tokens:     rl.config.Burst,
			refillRate: rl.config.RPS,
			lastRefill: time.Now(),
		}
		rl.buckets[clientID] = bucket
	}

	return bucket
}

// cleanup removes old buckets to prevent memory leaks
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for clientID, bucket := range rl.buckets {
			bucket.mu.Lock()
			// Remove buckets that haven't been used for 10 minutes
			if now.Sub(bucket.lastRefill) > 10*time.Minute {
				delete(rl.buckets, clientID)
			}
			bucket.mu.Unlock()
		}
		rl.mu.Unlock()
	}
}

// GetStats returns rate limiter statistics
func (rl *RateLimiter) GetStats() map[string]interface{} {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	stats := make(map[string]interface{})
	stats["active_buckets"] = len(rl.buckets)
	stats["rps"] = rl.config.RPS
	stats["burst"] = rl.config.Burst

	return stats
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
