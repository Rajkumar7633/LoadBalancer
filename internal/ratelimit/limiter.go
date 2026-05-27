package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"

	"loadbalancer/internal/config"
	"loadbalancer/internal/logger"
	"loadbalancer/internal/metrics"
)

type Limiter struct {
	config      config.RateLimitConfig
	logger      *logger.Logger
	metrics     *metrics.Collector
	buckets     map[string]*TokenBucket
	mu          sync.RWMutex
	cleanupTick *time.Ticker
}

type TokenBucket struct {
	capacity     int
	tokens       int
	refillRate   int
	lastRefill   time.Time
	window       time.Duration
	requestCount int
	windowStart  time.Time
	mu           sync.Mutex
}

type RateLimitResult struct {
	Allowed    bool
	Remaining  int
	ResetTime  time.Time
	RetryAfter time.Duration
}

func NewLimiter(cfg config.RateLimitConfig, logger *logger.Logger, metrics *metrics.Collector) *Limiter {
	limiter := &Limiter{
		config:  cfg,
		logger:  logger,
		metrics: metrics,
		buckets: make(map[string]*TokenBucket),
	}

	// Start cleanup goroutine
	limiter.cleanupTick = time.NewTicker(5 * time.Minute)
	go limiter.cleanup()

	return limiter
}

func (l *Limiter) Allow(r *http.Request) bool {
	clientKey := l.getClientKey(r)

	// Get or create token bucket for client
	bucket := l.getBucket(clientKey)

	// Check if request is allowed
	allowed := bucket.consume()

	if !allowed {
		l.logger.Warn("Rate limit exceeded", "client_key", clientKey, "tokens", bucket.tokens)
		l.metrics.RecordRateLimitHit(clientKey, "token_bucket")
	}

	return allowed
}

func (l *Limiter) GetRateLimitStatus(r *http.Request) RateLimitResult {
	clientKey := l.getClientKey(r)
	bucket := l.getBucket(clientKey)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	remaining := bucket.tokens
	if remaining < 0 {
		remaining = 0
	}

	resetTime := bucket.lastRefill.Add(time.Second)
	retryAfter := time.Second

	return RateLimitResult{
		Allowed:    bucket.tokens > 0,
		Remaining:  remaining,
		ResetTime:  resetTime,
		RetryAfter: retryAfter,
	}
}

func (l *Limiter) getClientKey(r *http.Request) string {
	// Try to get client IP from various headers
	clientIP := getClientIP(r)

	// Additional identification from headers for more sophisticated rate limiting
	userAgent := r.Header.Get("User-Agent")
	apiKey := r.Header.Get("X-API-Key")

	// Create composite key for more precise rate limiting
	key := clientIP
	if apiKey != "" {
		key = "api:" + apiKey
	} else if userAgent != "" {
		hash := sha256.Sum256([]byte(userAgent))
		key = "ua:" + hex.EncodeToString(hash[:8])
	}

	return key
}

func (l *Limiter) getBucket(clientKey string) *TokenBucket {
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, exists := l.buckets[clientKey]
	if !exists {
		bucket = &TokenBucket{
			capacity:    l.config.BurstSize,
			tokens:      l.config.BurstSize,
			refillRate:  l.config.RequestsPerSecond,
			lastRefill:  time.Now(),
			window:      l.config.Window,
			windowStart: time.Now(),
		}
		l.buckets[clientKey] = bucket
	}

	return bucket
}

func (b *TokenBucket) consume() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()

	// Refill tokens based on time elapsed
	elapsed := now.Sub(b.lastRefill)
	tokensToAdd := int(elapsed.Seconds() * float64(b.refillRate))

	if tokensToAdd > 0 {
		b.tokens += tokensToAdd
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.lastRefill = now
	}

	// Check window-based rate limiting
	if now.Sub(b.windowStart) >= b.window {
		b.requestCount = 0
		b.windowStart = now
	}

	// Check if request is allowed
	if b.tokens > 0 && b.requestCount < b.capacity {
		b.tokens--
		b.requestCount++
		return true
	}

	return false
}

func (l *Limiter) cleanup() {
	for {
		select {
		case <-l.cleanupTick.C:
			l.cleanupExpiredBuckets()
		}
	}
}

func (l *Limiter) cleanupExpiredBuckets() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	expiredKeys := make([]string, 0)

	for key, bucket := range l.buckets {
		bucket.mu.Lock()
		// Remove buckets that haven't been used for 10 minutes
		if now.Sub(bucket.lastRefill) > 10*time.Minute {
			expiredKeys = append(expiredKeys, key)
		}
		bucket.mu.Unlock()
	}

	for _, key := range expiredKeys {
		delete(l.buckets, key)
	}

	if len(expiredKeys) > 0 {
		l.logger.Debug("Cleaned up expired rate limit buckets", "count", len(expiredKeys))
	}
}

func (l *Limiter) Stop() {
	if l.cleanupTick != nil {
		l.cleanupTick.Stop()
	}
}

func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if idx := len(xff); idx > 0 {
			if commaIdx := 0; commaIdx < idx {
				for i, c := range xff {
					if c == ',' {
						commaIdx = i
						break
					}
				}
				if commaIdx > 0 {
					return xff[:commaIdx]
				}
			}
			return xff
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Check Cloudflare headers
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return cf
	}

	// Parse RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}

// Advanced rate limiting features

type AdvancedLimiter struct {
	*Limiter
	globalBucket *TokenBucket
	geoLimits    map[string]*TokenBucket
	pathLimits   map[string]*TokenBucket
	userLimits   map[string]*TokenBucket
}

func NewAdvancedLimiter(cfg config.RateLimitConfig, logger *logger.Logger, metrics *metrics.Collector) *AdvancedLimiter {
	baseLimiter := NewLimiter(cfg, logger, metrics)

	advanced := &AdvancedLimiter{
		Limiter: baseLimiter,
		globalBucket: &TokenBucket{
			capacity:    cfg.BurstSize * 10, // Global limit is higher
			tokens:      cfg.BurstSize * 10,
			refillRate:  cfg.RequestsPerSecond * 10,
			lastRefill:  time.Now(),
			window:      cfg.Window,
			windowStart: time.Now(),
		},
		geoLimits:  make(map[string]*TokenBucket),
		pathLimits: make(map[string]*TokenBucket),
		userLimits: make(map[string]*TokenBucket),
	}

	return advanced
}

func (al *AdvancedLimiter) AllowAdvanced(r *http.Request) bool {
	// Check global rate limit first
	if !al.globalBucket.consume() {
		al.logger.Warn("Global rate limit exceeded")
		al.metrics.RecordRateLimitHit("global", "global")
		return false
	}

	// Check path-specific rate limits
	pathKey := r.URL.Path
	pathBucket := al.getPathBucket(pathKey)
	if !pathBucket.consume() {
		al.logger.Warn("Path rate limit exceeded", "path", pathKey)
		al.metrics.RecordRateLimitHit("path:"+pathKey, "path")
		return false
	}

	// Check user-specific rate limits if user is authenticated
	if userID := r.Header.Get("X-User-ID"); userID != "" {
		userBucket := al.getUserBucket(userID)
		if !userBucket.consume() {
			al.logger.Warn("User rate limit exceeded", "user_id", userID)
			al.metrics.RecordRateLimitHit("user:"+userID, "user")
			return false
		}
	}

	// Check geo-based rate limits
	if country := r.Header.Get("CF-IPCountry"); country != "" {
		geoBucket := al.getGeoBucket(country)
		if !geoBucket.consume() {
			al.logger.Warn("Geo rate limit exceeded", "country", country)
			al.metrics.RecordRateLimitHit("geo:"+country, "geo")
			return false
		}
	}

	// Fall back to standard client-based rate limiting
	return al.Allow(r)
}

func (al *AdvancedLimiter) getPathBucket(path string) *TokenBucket {
	al.mu.Lock()
	defer al.mu.Unlock()

	bucket, exists := al.pathLimits[path]
	if !exists {
		bucket = &TokenBucket{
			capacity:    al.config.BurstSize / 2, // Path limits are more restrictive
			tokens:      al.config.BurstSize / 2,
			refillRate:  al.config.RequestsPerSecond / 2,
			lastRefill:  time.Now(),
			window:      al.config.Window,
			windowStart: time.Now(),
		}
		al.pathLimits[path] = bucket
	}

	return bucket
}

func (al *AdvancedLimiter) getUserBucket(userID string) *TokenBucket {
	al.mu.Lock()
	defer al.mu.Unlock()

	bucket, exists := al.userLimits[userID]
	if !exists {
		bucket = &TokenBucket{
			capacity:    al.config.BurstSize * 2, // User limits are more lenient
			tokens:      al.config.BurstSize * 2,
			refillRate:  al.config.RequestsPerSecond * 2,
			lastRefill:  time.Now(),
			window:      al.config.Window,
			windowStart: time.Now(),
		}
		al.userLimits[userID] = bucket
	}

	return bucket
}

func (al *AdvancedLimiter) getGeoBucket(country string) *TokenBucket {
	al.mu.Lock()
	defer al.mu.Unlock()

	bucket, exists := al.geoLimits[country]
	if !exists {
		bucket = &TokenBucket{
			capacity:    al.config.BurstSize * 5, // Geo limits are very lenient
			tokens:      al.config.BurstSize * 5,
			refillRate:  al.config.RequestsPerSecond * 5,
			lastRefill:  time.Now(),
			window:      al.config.Window,
			windowStart: time.Now(),
		}
		al.geoLimits[country] = bucket
	}

	return bucket
}
