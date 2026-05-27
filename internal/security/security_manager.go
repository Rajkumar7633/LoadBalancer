package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// SecurityManager handles all security-related functionality
type SecurityManager struct {
	config SecurityConfig
	logger *zap.Logger

	// Security components
	rateLimiter *RateLimiter
	authManager *AuthManager
	wafManager  *WAFManager
	certManager *CertificateManager
	auditLogger *AuditLogger

	// Runtime state
	mu      sync.RWMutex
	stopped bool
	metrics SecurityMetrics
}

// SecurityConfig contains security configuration
type SecurityConfig struct {
	// Rate limiting
	RateLimitEnabled bool          `json:"rate_limit_enabled"`
	RateLimitRPS     int           `json:"rate_limit_rps"`
	RateLimitBurst   int           `json:"rate_limit_burst"`
	RateLimitWindow  time.Duration `json:"rate_limit_window"`

	// Authentication
	AuthEnabled        bool     `json:"auth_enabled"`
	AuthMethods        []string `json:"auth_methods"` // jwt, apikey, oauth2, basic
	JWTSecret          string   `json:"jwt_secret"`
	APIKeyHeader       string   `json:"api_key_header"`
	OAuth2Provider     string   `json:"oauth2_provider"`
	OAuth2ClientID     string   `json:"oauth2_client_id"`
	OAuth2ClientSecret string   `json:"oauth2_client_secret"`

	// Web Application Firewall
	WAFEnabled bool     `json:"waf_enabled"`
	WAFRules   []string `json:"waf_rules"`
	WAFMode    string   `json:"waf_mode"` // block, monitor, learn

	// TLS/SSL
	TLSMinVersion    string   `json:"tls_min_version"`
	TLSCiphers       []string `json:"tls_ciphers"`
	AutoCertRenewal  bool     `json:"auto_cert_renewal"`
	CertRotationDays int      `json:"cert_rotation_days"`

	// Security headers
	SecurityHeaders SecurityHeadersConfig `json:"security_headers"`

	// IP filtering
	IPWhitelist []string `json:"ip_whitelist"`
	IPBlacklist []string `json:"ip_blacklist"`

	// Audit logging
	AuditEnabled       bool   `json:"audit_enabled"`
	AuditLogPath       string `json:"audit_log_path"`
	AuditRetentionDays int    `json:"audit_retention_days"`

	// General security
	MaxRequestSize int64         `json:"max_request_size"`
	MaxHeaderSize  int           `json:"max_header_size"`
	Timeout        time.Duration `json:"timeout"`

	// Logging
	Logger *zap.Logger `json:"-"`
}

// SecurityHeadersConfig defines security headers
type SecurityHeadersConfig struct {
	XFrameOptions       string `json:"x_frame_options"`
	XContentTypeOptions string `json:"x_content_type_options"`
	XSSProtection       string `json:"xss_protection"`
	StrictTransport     string `json:"strict_transport_security"`
	ContentSecurity     string `json:"content_security_policy"`
	ReferrerPolicy      string `json:"referrer_policy"`
}

// SecurityMetrics tracks security-related metrics
type SecurityMetrics struct {
	TotalRequests   int64     `json:"total_requests"`
	BlockedRequests int64     `json:"blocked_requests"`
	AuthFailures    int64     `json:"auth_failures"`
	RateLimitHits   int64     `json:"rate_limit_hits"`
	WAFBlocks       int64     `json:"waf_blocks"`
	SuspiciousIPs   int64     `json:"suspicious_ips"`
	CertRotations   int64     `json:"cert_rotations"`
	LastUpdated     time.Time `json:"last_updated"`
}

// SecurityContext holds security context for a request
type SecurityContext struct {
	ClientIP      string
	UserAgent     string
	RequestID     string
	Authenticated bool
	UserID        string
	Roles         []string
	RiskScore     float64
	Blocked       bool
	BlockReason   string
	Headers       map[string]string
	Metadata      map[string]interface{}
}

// NewSecurityManager creates a new security manager
func NewSecurityManager(config SecurityConfig) (*SecurityManager, error) {
	sm := &SecurityManager{
		config: config,
		logger: config.Logger,
		metrics: SecurityMetrics{
			LastUpdated: time.Now(),
		},
	}

	// Initialize security components
	if err := sm.initializeComponents(); err != nil {
		return nil, fmt.Errorf("failed to initialize security components: %w", err)
	}

	// Start background tasks
	go sm.startBackgroundTasks()

	sm.logger.Info("Security manager initialized successfully",
		zap.Bool("rate_limit", config.RateLimitEnabled),
		zap.Bool("auth", config.AuthEnabled),
		zap.Bool("waf", config.WAFEnabled))

	return sm, nil
}

// initializeComponents initializes all security components
func (sm *SecurityManager) initializeComponents() error {
	var err error

	// Initialize rate limiter
	if sm.config.RateLimitEnabled {
		sm.rateLimiter, err = NewRateLimiter(RateLimiterConfig{
			RPS:    sm.config.RateLimitRPS,
			Burst:  sm.config.RateLimitBurst,
			Window: sm.config.RateLimitWindow,
			Logger: sm.logger,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize rate limiter: %w", err)
		}
	}

	// Initialize auth manager
	if sm.config.AuthEnabled {
		sm.authManager, err = NewAuthManager(AuthManagerConfig{
			Methods:            sm.config.AuthMethods,
			JWTSecret:          sm.config.JWTSecret,
			APIKeyHeader:       sm.config.APIKeyHeader,
			OAuth2Provider:     sm.config.OAuth2Provider,
			OAuth2ClientID:     sm.config.OAuth2ClientID,
			OAuth2ClientSecret: sm.config.OAuth2ClientSecret,
			Logger:             sm.logger,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize auth manager: %w", err)
		}
	}

	// Initialize WAF manager
	if sm.config.WAFEnabled {
		sm.wafManager, err = NewWAFManager(WAFManagerConfig{
			Rules:  sm.config.WAFRules,
			Mode:   sm.config.WAFMode,
			Logger: sm.logger,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize WAF manager: %w", err)
		}
	}

	// Initialize certificate manager
	sm.certManager, err = NewCertificateManager(CertificateManagerConfig{
		AutoRenewal:   sm.config.AutoCertRenewal,
		RotationDays:  sm.config.CertRotationDays,
		TLSMinVersion: sm.config.TLSMinVersion,
		TLSCiphers:    sm.config.TLSCiphers,
		Logger:        sm.logger,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize certificate manager: %w", err)
	}

	// Initialize audit logger
	if sm.config.AuditEnabled {
		sm.auditLogger, err = NewAuditLogger(AuditLoggerConfig{
			LogPath:       sm.config.AuditLogPath,
			RetentionDays: sm.config.AuditRetentionDays,
			Logger:        sm.logger,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize audit logger: %w", err)
		}
	}

	return nil
}

// ProcessRequest processes a request through all security layers
func (sm *SecurityManager) ProcessRequest(ctx context.Context, req *http.Request) (*SecurityContext, error) {
	securityCtx := &SecurityContext{
		ClientIP:  sm.getClientIP(req),
		UserAgent: req.UserAgent(),
		RequestID: sm.generateRequestID(),
		Headers:   make(map[string]string),
		Metadata:  make(map[string]interface{}),
	}

	// Copy headers
	for name, values := range req.Header {
		if len(values) > 0 {
			securityCtx.Headers[name] = values[0]
		}
	}

	// Update metrics
	atomic.AddInt64(&sm.metrics.TotalRequests, 1)

	// Layer 1: IP filtering
	if err := sm.checkIPFiltering(securityCtx); err != nil {
		return securityCtx, err
	}

	// Layer 2: Rate limiting
	if sm.config.RateLimitEnabled {
		if err := sm.rateLimiter.CheckLimit(securityCtx.ClientIP); err != nil {
			securityCtx.Blocked = true
			securityCtx.BlockReason = "rate_limit_exceeded"
			atomic.AddInt64(&sm.metrics.RateLimitHits, 1)
			sm.logSecurityEvent("rate_limit_hit", securityCtx, nil)
			return securityCtx, err
		}
	}

	// Layer 3: Web Application Firewall
	if sm.config.WAFEnabled {
		if err := sm.wafManager.CheckRequest(req, securityCtx); err != nil {
			securityCtx.Blocked = true
			securityCtx.BlockReason = "waf_block"
			atomic.AddInt64(&sm.metrics.WAFBlocks, 1)
			sm.logSecurityEvent("waf_block", securityCtx, nil)
			return securityCtx, err
		}
	}

	// Layer 4: Authentication
	if sm.config.AuthEnabled {
		if err := sm.authManager.Authenticate(req, securityCtx); err != nil {
			securityCtx.Blocked = true
			securityCtx.BlockReason = "auth_failure"
			atomic.AddInt64(&sm.metrics.AuthFailures, 1)
			sm.logSecurityEvent("auth_failure", securityCtx, nil)
			return securityCtx, err
		}
	}

	// Layer 5: Risk assessment
	sm.assessRisk(securityCtx)

	// Log successful security processing
	sm.logSecurityEvent("security_check_passed", securityCtx, nil)

	return securityCtx, nil
}

// checkIPFiltering checks if the client IP is allowed
func (sm *SecurityManager) checkIPFiltering(securityCtx *SecurityContext) error {
	clientIP := securityCtx.ClientIP

	// Check blacklist
	for _, blacklistedIP := range sm.config.IPBlacklist {
		if sm.matchIP(clientIP, blacklistedIP) {
			securityCtx.Blocked = true
			securityCtx.BlockReason = "ip_blacklisted"
			atomic.AddInt64(&sm.metrics.SuspiciousIPs, 1)
			sm.logSecurityEvent("ip_blacklisted", securityCtx, nil)
			return fmt.Errorf("client IP %s is blacklisted", clientIP)
		}
	}

	// Check whitelist (if configured)
	if len(sm.config.IPWhitelist) > 0 {
		allowed := false
		for _, whitelistedIP := range sm.config.IPWhitelist {
			if sm.matchIP(clientIP, whitelistedIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			securityCtx.Blocked = true
			securityCtx.BlockReason = "ip_not_whitelisted"
			atomic.AddInt64(&sm.metrics.SuspiciousIPs, 1)
			sm.logSecurityEvent("ip_not_whitelisted", securityCtx, nil)
			return fmt.Errorf("client IP %s is not whitelisted", clientIP)
		}
	}

	return nil
}

// matchIP checks if an IP matches a pattern (supports CIDR notation)
func (sm *SecurityManager) matchIP(ip, pattern string) bool {
	// Simple exact match for now
	// In production, implement proper CIDR matching
	return ip == pattern
}

// assessRisk assesses the risk score for the request
func (sm *SecurityManager) assessRisk(securityCtx *SecurityContext) {
	riskScore := 0.0

	// Risk factors
	if !securityCtx.Authenticated {
		riskScore += 0.3
	}

	if strings.Contains(strings.ToLower(securityCtx.UserAgent), "bot") {
		riskScore += 0.2
	}

	if securityCtx.ClientIP == "" {
		riskScore += 0.1
	}

	// Add more risk factors as needed

	securityCtx.RiskScore = riskScore
}

// ApplySecurityHeaders applies security headers to the response
func (sm *SecurityManager) ApplySecurityHeaders(w http.ResponseWriter) {
	headers := sm.config.SecurityHeaders

	if headers.XFrameOptions != "" {
		w.Header().Set("X-Frame-Options", headers.XFrameOptions)
	}
	if headers.XContentTypeOptions != "" {
		w.Header().Set("X-Content-Type-Options", headers.XContentTypeOptions)
	}
	if headers.XSSProtection != "" {
		w.Header().Set("X-XSS-Protection", headers.XSSProtection)
	}
	if headers.StrictTransport != "" {
		w.Header().Set("Strict-Transport-Security", headers.StrictTransport)
	}
	if headers.ContentSecurity != "" {
		w.Header().Set("Content-Security-Policy", headers.ContentSecurity)
	}
	if headers.ReferrerPolicy != "" {
		w.Header().Set("Referrer-Policy", headers.ReferrerPolicy)
	}
}

// GetTLSConfig returns the TLS configuration
func (sm *SecurityManager) GetTLSConfig() (*tls.Config, error) {
	return sm.certManager.GetTLSConfig()
}

// GetMetrics returns security metrics
func (sm *SecurityManager) GetMetrics() SecurityMetrics {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.metrics
}

// logSecurityEvent logs security events
func (sm *SecurityManager) logSecurityEvent(event string, securityCtx *SecurityContext, err error) {
	if sm.config.AuditEnabled && sm.auditLogger != nil {
		sm.auditLogger.LogEvent(event, securityCtx, err)
	}

	sm.logger.Info("Security event",
		zap.String("event", event),
		zap.String("client_ip", securityCtx.ClientIP),
		zap.String("user_agent", securityCtx.UserAgent),
		zap.String("request_id", securityCtx.RequestID),
		zap.Bool("blocked", securityCtx.Blocked),
		zap.String("block_reason", securityCtx.BlockReason),
		zap.Float64("risk_score", securityCtx.RiskScore),
		zap.Error(err))
}

// getClientIP extracts the real client IP from request
func (sm *SecurityManager) getClientIP(req *http.Request) string {
	// Check X-Forwarded-For header
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}

	// Check X-Real-IP header
	if xri := req.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	return req.RemoteAddr
}

// generateRequestID generates a unique request ID
func (sm *SecurityManager) generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// startBackgroundTasks starts background security tasks
func (sm *SecurityManager) startBackgroundTasks() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.updateMetrics()
		}
	}
}

// updateMetrics updates security metrics
func (sm *SecurityManager) updateMetrics() {
	sm.mu.Lock()
	sm.metrics.LastUpdated = time.Now()
	sm.mu.Unlock()
}

// Stop stops the security manager
func (sm *SecurityManager) Stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.stopped = true

	if sm.auditLogger != nil {
		sm.auditLogger.Close()
	}

	sm.logger.Info("Security manager stopped")
}

// GenerateSelfSignedCert generates a self-signed certificate for testing
func GenerateSelfSignedCert(host string) ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour) // 1 year

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Load Balancer"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	return certPEM, privPEM, nil
}
