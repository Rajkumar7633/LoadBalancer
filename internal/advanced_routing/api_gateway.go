package advanced_routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type APIGateway struct {
	authManager  *AuthManager
	rateLimiter  *AdvancedRateLimiter
	quotaManager *QuotaManager
	keyManager   *APIKeyManager
	config       APIGatewayConfig
	mu           sync.RWMutex
}

type APIGatewayConfig struct {
	Enabled          bool            `json:"enabled"`
	DefaultQuota     QuotaConfig     `json:"default_quota"`
	RateLimitConfig  RateLimitConfig `json:"rate_limit_config"`
	AuthRequired     bool            `json:"auth_required"`
	KeyValidation    bool            `json:"key_validation"`
	QuotaEnforcement bool            `json:"quota_enforcement"`
	MetricsEnabled   bool            `json:"metrics_enabled"`
}

type AuthManager struct {
	strategies map[string]AuthStrategy
	jwtSecret  string
	mu         sync.RWMutex
}

type AuthStrategy interface {
	Authenticate(req *http.Request) (*AuthContext, error)
	GetName() string
}

type AuthContext struct {
	APIKey      string                 `json:"api_key"`
	UserID      string                 `json:"user_id"`
	TenantID    string                 `json:"tenant_id"`
	Permissions []string               `json:"permissions"`
	Metadata    map[string]interface{} `json:"metadata"`
	ValidUntil  time.Time              `json:"valid_until"`
}

type AdvancedRateLimiter struct {
	limiters map[string]*TokenBucket
	config   RateLimitConfig
	mu       sync.RWMutex
}

type RateLimitConfig struct {
	Algorithm       string        `json:"algorithm"` // "token_bucket", "sliding_window", "fixed_window"
	DefaultRate     int           `json:"default_rate"`
	DefaultBurst    int           `json:"default_burst"`
	WindowDuration  time.Duration `json:"window_duration"`
	CleanupInterval time.Duration `json:"cleanup_interval"`
}

type TokenBucket struct {
	capacity   int
	tokens     int
	refillRate int
	lastRefill time.Time
	mu         sync.Mutex
}

type QuotaManager struct {
	quotas map[string]*Quota
	config QuotaConfig
	mu     sync.RWMutex
}

type QuotaConfig struct {
	TimeWindow   time.Duration `json:"time_window"`
	DefaultLimit int64         `json:"default_limit"`
	ResetPolicy  string        `json:"reset_policy"` // "daily", "weekly", "monthly", "rolling"
}

type Quota struct {
	Key         string    `json:"key"`
	Limit       int64     `json:"limit"`
	Used        int64     `json:"used"`
	ResetTime   time.Time `json:"reset_time"`
	WindowStart time.Time `json:"window_start"`
}

type APIKeyManager struct {
	keys map[string]*APIKey
	mu   sync.RWMutex
}

type APIKey struct {
	Key         string                 `json:"key"`
	Name        string                 `json:"name"`
	Active      bool                   `json:"active"`
	CreatedAt   time.Time              `json:"created_at"`
	ExpiresAt   *time.Time             `json:"expires_at"`
	RateLimit   *RateLimitConfig       `json:"rate_limit"`
	Quota       *QuotaConfig           `json:"quota"`
	Permissions []string               `json:"permissions"`
	Metadata    map[string]interface{} `json:"metadata"`
	Usage       APIKeyUsage            `json:"usage"`
}

type APIKeyUsage struct {
	TotalRequests   int64     `json:"total_requests"`
	LastUsed        time.Time `json:"last_used"`
	DailyRequests   int64     `json:"daily_requests"`
	MonthlyRequests int64     `json:"monthly_requests"`
}

type GatewayRequest struct {
	OriginalRequest *http.Request
	AuthContext     *AuthContext
	APIKey          *APIKey
	QuotaChecked    bool
	RateLimited     bool
	Permissions     []string
	Metadata        map[string]interface{}
}

func NewAPIGateway(config APIGatewayConfig) *APIGateway {
	return &APIGateway{
		authManager:  NewAuthManager(),
		rateLimiter:  NewAdvancedRateLimiter(config.RateLimitConfig),
		quotaManager: NewQuotaManager(config.DefaultQuota),
		keyManager:   NewAPIKeyManager(),
		config:       config,
	}
}

func (ag *APIGateway) ProcessRequest(ctx context.Context, req *http.Request) (*GatewayRequest, error) {
	gatewayReq := &GatewayRequest{
		OriginalRequest: req,
		Metadata:        make(map[string]interface{}),
	}

	// Step 1: Authentication
	if ag.config.AuthRequired {
		authCtx, err := ag.authManager.Authenticate(req)
		if err != nil {
			return nil, fmt.Errorf("authentication failed: %w", err)
		}
		gatewayReq.AuthContext = authCtx
	}

	// Step 2: API Key Validation
	if ag.config.KeyValidation {
		apiKey, err := ag.extractAndValidateAPIKey(req)
		if err != nil {
			return nil, fmt.Errorf("API key validation failed: %w", err)
		}
		gatewayReq.APIKey = apiKey
		gatewayReq.Permissions = apiKey.Permissions
	}

	// Step 3: Rate Limiting
	if gatewayReq.APIKey != nil {
		if !ag.rateLimiter.Allow(gatewayReq.APIKey.Key, gatewayReq.APIKey.RateLimit) {
			gatewayReq.RateLimited = true
			return gatewayReq, fmt.Errorf("rate limit exceeded")
		}
	}

	// Step 4: Quota Checking
	if ag.config.QuotaEnforcement && gatewayReq.APIKey != nil {
		if !ag.quotaManager.CheckQuota(gatewayReq.APIKey.Key, gatewayReq.APIKey.Quota) {
			gatewayReq.QuotaChecked = true
			return gatewayReq, fmt.Errorf("quota exceeded")
		}
		ag.quotaManager.ConsumeQuota(gatewayReq.APIKey.Key, 1)
	}

	// Update usage statistics
	if gatewayReq.APIKey != nil {
		ag.keyManager.UpdateUsage(gatewayReq.APIKey.Key)
	}

	return gatewayReq, nil
}

func (ag *APIGateway) extractAndValidateAPIKey(req *http.Request) (*APIKey, error) {
	// Extract API key from various sources
	apiKey := ag.extractAPIKey(req)
	if apiKey == "" {
		return nil, fmt.Errorf("API key required")
	}

	// Validate API key
	key, err := ag.keyManager.ValidateKey(apiKey)
	if err != nil {
		return nil, err
	}

	return key, nil
}

func (ag *APIGateway) extractAPIKey(req *http.Request) string {
	// Check Authorization header (Bearer token)
	authHeader := req.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// Check X-API-Key header
	if apiKey := req.Header.Get("X-API-Key"); apiKey != "" {
		return apiKey
	}

	// Check query parameter
	if apiKey := req.URL.Query().Get("api_key"); apiKey != "" {
		return apiKey
	}

	return ""
}

func (ag *APIGateway) CreateAPIKey(name string, config APIKeyConfig) (*APIKey, error) {
	return ag.keyManager.CreateKey(name, config)
}

func (ag *APIGateway) RevokeAPIKey(key string) error {
	return ag.keyManager.RevokeKey(key)
}

func (ag *APIGateway) GetAPIKeyInfo(key string) (*APIKey, error) {
	return ag.keyManager.GetKeyInfo(key)
}

func (ag *APIGateway) GetUsageStats(key string) (*APIKeyUsage, error) {
	return ag.keyManager.GetUsageStats(key)
}

func (ag *APIGateway) GetGatewayStats() GatewayStats {
	ag.mu.RLock()
	defer ag.mu.RUnlock()

	return GatewayStats{
		TotalAPIKeys:    ag.keyManager.GetKeyCount(),
		ActiveAPIKeys:   ag.keyManager.GetActiveKeyCount(),
		TotalRequests:   ag.rateLimiter.GetTotalRequests(),
		RateLimitedReqs: ag.rateLimiter.GetRateLimitedRequests(),
		QuotaExceeded:   ag.quotaManager.GetQuotaExceededCount(),
	}
}

type GatewayStats struct {
	TotalAPIKeys    int   `json:"total_api_keys"`
	ActiveAPIKeys   int   `json:"active_api_keys"`
	TotalRequests   int64 `json:"total_requests"`
	RateLimitedReqs int64 `json:"rate_limited_requests"`
	QuotaExceeded   int64 `json:"quota_exceeded"`
}

type APIKeyConfig struct {
	Name        string                 `json:"name"`
	ExpiresAt   *time.Time             `json:"expires_at"`
	RateLimit   *RateLimitConfig       `json:"rate_limit"`
	Quota       *QuotaConfig           `json:"quota"`
	Permissions []string               `json:"permissions"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// AuthManager implementation
func NewAuthManager() *AuthManager {
	return &AuthManager{
		strategies: make(map[string]AuthStrategy),
	}
}

func (am *AuthManager) AddStrategy(strategy AuthStrategy) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.strategies[strategy.GetName()] = strategy
}

func (am *AuthManager) Authenticate(req *http.Request) (*AuthContext, error) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	// Try each authentication strategy
	for _, strategy := range am.strategies {
		authCtx, err := strategy.Authenticate(req)
		if err == nil {
			return authCtx, nil
		}
	}

	return nil, fmt.Errorf("no valid authentication method found")
}

// JWT Auth Strategy
type JWTAuthStrategy struct {
	secret string
}

func NewJWTAuthStrategy(secret string) *JWTAuthStrategy {
	return &JWTAuthStrategy{secret: secret}
}

func (jas *JWTAuthStrategy) Authenticate(req *http.Request) (*AuthContext, error) {
	token := jas.extractToken(req)
	if token == "" {
		return nil, fmt.Errorf("no JWT token found")
	}

	// Simplified JWT validation - in practice, use a proper JWT library
	claims, err := jas.validateToken(token)
	if err != nil {
		return nil, err
	}

	return &AuthContext{
		APIKey:      claims["api_key"].(string),
		UserID:      claims["user_id"].(string),
		TenantID:    claims["tenant_id"].(string),
		Permissions: claims["permissions"].([]string),
		Metadata:    claims["metadata"].(map[string]interface{}),
		ValidUntil:  time.Unix(int64(claims["exp"].(float64)), 0),
	}, nil
}

func (jas *JWTAuthStrategy) extractToken(req *http.Request) string {
	authHeader := req.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return ""
}

func (jas *JWTAuthStrategy) validateToken(token string) (map[string]interface{}, error) {
	// Simplified JWT validation - in practice, use a proper JWT library
	// For now, return mock claims
	return map[string]interface{}{
		"api_key":     "demo_key",
		"user_id":     "user123",
		"tenant_id":   "tenant456",
		"permissions": []string{"read", "write"},
		"metadata":    map[string]interface{}{"role": "admin"},
		"exp":         float64(time.Now().Add(time.Hour).Unix()),
	}, nil
}

func (jas *JWTAuthStrategy) GetName() string {
	return "jwt"
}

// AdvancedRateLimiter implementation
func NewAdvancedRateLimiter(config RateLimitConfig) *AdvancedRateLimiter {
	limiter := &AdvancedRateLimiter{
		limiters: make(map[string]*TokenBucket),
		config:   config,
	}

	// Start cleanup routine
	go limiter.startCleanup()

	return limiter
}

func (arl *AdvancedRateLimiter) Allow(key string, config *RateLimitConfig) bool {
	arl.mu.Lock()
	defer arl.mu.Unlock()

	// Use provided config or default
	rateLimit := config
	if rateLimit == nil {
		rateLimit = &arl.config
	}

	// Get or create token bucket
	bucket, exists := arl.limiters[key]
	if !exists {
		bucket = &TokenBucket{
			capacity:   rateLimit.DefaultBurst,
			tokens:     rateLimit.DefaultBurst,
			refillRate: rateLimit.DefaultRate,
			lastRefill: time.Now(),
		}
		arl.limiters[key] = bucket
	}

	return bucket.consume()
}

func (arl *AdvancedRateLimiter) startCleanup() {
	ticker := time.NewTicker(arl.config.CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		arl.cleanup()
	}
}

func (arl *AdvancedRateLimiter) cleanup() {
	arl.mu.Lock()
	defer arl.mu.Unlock()

	for key, bucket := range arl.limiters {
		// Remove inactive buckets (simplified logic)
		if time.Since(bucket.lastRefill) > time.Hour {
			delete(arl.limiters, key)
		}
	}
}

func (arl *AdvancedRateLimiter) GetTotalRequests() int64 {
	// This would be tracked in a real implementation
	return 0
}

func (arl *AdvancedRateLimiter) GetRateLimitedRequests() int64 {
	// This would be tracked in a real implementation
	return 0
}

// TokenBucket implementation
func (tb *TokenBucket) consume() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.refill()

	if tb.tokens > 0 {
		tb.tokens--
		return true
	}
	return false
}

func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)
	tokensToAdd := int(elapsed.Seconds()) * tb.refillRate

	if tokensToAdd > 0 {
		tb.tokens = min(tb.tokens+tokensToAdd, tb.capacity)
		tb.lastRefill = now
	}
}

// QuotaManager implementation
func NewQuotaManager(config QuotaConfig) *QuotaManager {
	return &QuotaManager{
		quotas: make(map[string]*Quota),
		config: config,
	}
}

func (qm *QuotaManager) CheckQuota(key string, config *QuotaConfig) bool {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	quotaConfig := *config
	if config == nil {
		quotaConfig = qm.config
	}

	quota, exists := qm.quotas[key]
	if !exists {
		quota = &Quota{
			Key:         key,
			Limit:       quotaConfig.DefaultLimit,
			Used:        0,
			ResetTime:   qm.calculateResetTime(quotaConfig),
			WindowStart: time.Now(),
		}
		qm.quotas[key] = quota
	}

	// Check if quota needs reset
	if time.Now().After(quota.ResetTime) {
		quota.Used = 0
		quota.WindowStart = time.Now()
		quota.ResetTime = qm.calculateResetTime(quotaConfig)
	}

	return quota.Used < quota.Limit
}

func (qm *QuotaManager) ConsumeQuota(key string, amount int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if quota, exists := qm.quotas[key]; exists {
		quota.Used += amount
	}
}

func (qm *QuotaManager) calculateResetTime(config QuotaConfig) time.Time {
	now := time.Now()
	switch config.ResetPolicy {
	case "daily":
		return now.Add(24 * time.Hour)
	case "weekly":
		return now.Add(7 * 24 * time.Hour)
	case "monthly":
		return now.Add(30 * 24 * time.Hour)
	default: // rolling
		return now.Add(config.TimeWindow)
	}
}

func (qm *QuotaManager) GetQuotaExceededCount() int64 {
	// This would be tracked in a real implementation
	return 0
}

// APIKeyManager implementation
func NewAPIKeyManager() *APIKeyManager {
	return &APIKeyManager{
		keys: make(map[string]*APIKey),
	}
}

func (akm *APIKeyManager) CreateKey(name string, config APIKeyConfig) (*APIKey, error) {
	akm.mu.Lock()
	defer akm.mu.Unlock()

	key := generateAPIKey()
	apiKey := &APIKey{
		Key:         key,
		Name:        name,
		Active:      true,
		CreatedAt:   time.Now(),
		ExpiresAt:   config.ExpiresAt,
		RateLimit:   config.RateLimit,
		Quota:       config.Quota,
		Permissions: config.Permissions,
		Metadata:    config.Metadata,
		Usage: APIKeyUsage{
			TotalRequests:   0,
			LastUsed:        time.Now(),
			DailyRequests:   0,
			MonthlyRequests: 0,
		},
	}

	akm.keys[key] = apiKey
	return apiKey, nil
}

func (akm *APIKeyManager) ValidateKey(key string) (*APIKey, error) {
	akm.mu.RLock()
	defer akm.mu.RUnlock()

	apiKey, exists := akm.keys[key]
	if !exists {
		return nil, fmt.Errorf("invalid API key")
	}

	if !apiKey.Active {
		return nil, fmt.Errorf("API key is inactive")
	}

	if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
		return nil, fmt.Errorf("API key has expired")
	}

	return apiKey, nil
}

func (akm *APIKeyManager) RevokeKey(key string) error {
	akm.mu.Lock()
	defer akm.mu.Unlock()

	apiKey, exists := akm.keys[key]
	if !exists {
		return fmt.Errorf("API key not found")
	}

	apiKey.Active = false
	return nil
}

func (akm *APIKeyManager) GetKeyInfo(key string) (*APIKey, error) {
	akm.mu.RLock()
	defer akm.mu.RUnlock()

	apiKey, exists := akm.keys[key]
	if !exists {
		return nil, fmt.Errorf("API key not found")
	}

	return apiKey, nil
}

func (akm *APIKeyManager) UpdateUsage(key string) {
	akm.mu.Lock()
	defer akm.mu.Unlock()

	apiKey, exists := akm.keys[key]
	if !exists {
		return
	}

	apiKey.Usage.TotalRequests++
	apiKey.Usage.LastUsed = time.Now()
	apiKey.Usage.DailyRequests++
	apiKey.Usage.MonthlyRequests++
}

func (akm *APIKeyManager) GetUsageStats(key string) (*APIKeyUsage, error) {
	akm.mu.RLock()
	defer akm.mu.RUnlock()

	apiKey, exists := akm.keys[key]
	if !exists {
		return nil, fmt.Errorf("API key not found")
	}

	return &apiKey.Usage, nil
}

func (akm *APIKeyManager) GetKeyCount() int {
	akm.mu.RLock()
	defer akm.mu.RUnlock()
	return len(akm.keys)
}

func (akm *APIKeyManager) GetActiveKeyCount() int {
	akm.mu.RLock()
	defer akm.mu.RUnlock()

	count := 0
	for _, key := range akm.keys {
		if key.Active {
			count++
		}
	}
	return count
}

// Helper functions
func generateAPIKey() string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(hash[:])[:32]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
