package tests

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"loadbalancer/internal/security"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// TestSecurityManager tests the complete security manager functionality
func TestSecurityManager(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create security configuration
	config := security.SecurityConfig{
		// Rate limiting
		RateLimitEnabled: true,
		RateLimitRPS:     100,
		RateLimitBurst:   200,
		RateLimitWindow:  time.Minute,

		// Authentication
		AuthEnabled:  true,
		AuthMethods:  []string{"jwt", "apikey"},
		JWTSecret:    "test-secret-key",
		APIKeyHeader: "X-API-Key",

		// Web Application Firewall
		WAFEnabled: true,
		WAFRules:   []string{"sql_injection", "xss"},
		WAFMode:    "block",

		// IP filtering
		IPBlacklist: []string{"192.168.1.100"},

		// Security headers
		SecurityHeaders: security.SecurityHeadersConfig{
			XFrameOptions:       "DENY",
			XContentTypeOptions: "nosniff",
			XSSProtection:       "1; mode=block",
			StrictTransport:     "max-age=31536000; includeSubDomains",
		},

		// General security
		MaxRequestSize: 10 * 1024 * 1024, // 10MB
		MaxHeaderSize:  8192,             // 8KB
		Timeout:        30 * time.Second,

		// Audit logging
		AuditEnabled:       true,
		AuditLogPath:       "/tmp/test_audit.log",
		AuditRetentionDays: 30,

		Logger: logger,
	}

	// Create security manager
	sm, err := security.NewSecurityManager(config)
	require.NoError(t, err)
	require.NotNil(t, sm)
	defer sm.Stop()

	t.Run("RateLimiting", func(t *testing.T) {
		// Create test request with valid API key
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-API-Key", "test-api-key-123") // Valid test key
		req.RemoteAddr = "192.168.1.1"

		// Process requests within rate limit
		for i := 0; i < 10; i++ {
			securityCtx, err := sm.ProcessRequest(context.Background(), req)
			assert.NoError(t, err)
			assert.False(t, securityCtx.Blocked)
		}

		// Note: In a real test, we would exceed the rate limit,
		// but for unit testing we'll keep it simple
	})

	t.Run("Authentication", func(t *testing.T) {
		// Test JWT authentication
		t.Run("JWT", func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer invalid-token")

			securityCtx, err := sm.ProcessRequest(context.Background(), req)
			assert.Error(t, err)
			assert.True(t, securityCtx.Blocked)
			assert.Equal(t, "auth_failure", securityCtx.BlockReason)
		})

		// Test API key authentication
		t.Run("APIKey", func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-API-Key", "invalid-key")

			securityCtx, err := sm.ProcessRequest(context.Background(), req)
			assert.Error(t, err)
			assert.True(t, securityCtx.Blocked)
			assert.Equal(t, "auth_failure", securityCtx.BlockReason)
		})
	})

	t.Run("WAFProtection", func(t *testing.T) {
		// Test SQL injection protection
		t.Run("SQLInjection", func(t *testing.T) {
			req := httptest.NewRequest("GET", "/search", nil)
			req.URL.RawQuery = "q=' OR 1=1 --"
			req.Header.Set("X-API-Key", "test-api-key-123") // Valid key to pass auth

			securityCtx, err := sm.ProcessRequest(context.Background(), req)
			assert.Error(t, err)
			assert.True(t, securityCtx.Blocked)
			assert.Equal(t, "waf_block", securityCtx.BlockReason)
		})

		// Test XSS protection
		t.Run("XSS", func(t *testing.T) {
			req := httptest.NewRequest("GET", "/comment", nil)
			req.URL.RawQuery = "comment=<script>alert('xss')</script>"
			req.Header.Set("X-API-Key", "test-api-key-123") // Valid key to pass auth

			securityCtx, err := sm.ProcessRequest(context.Background(), req)
			assert.Error(t, err)
			assert.True(t, securityCtx.Blocked)
			assert.Equal(t, "waf_block", securityCtx.BlockReason)
		})
	})

	t.Run("IPFiltering", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.100" // Blacklisted IP

		securityCtx, err := sm.ProcessRequest(context.Background(), req)
		assert.Error(t, err)
		assert.True(t, securityCtx.Blocked)
		assert.Equal(t, "ip_blacklisted", securityCtx.BlockReason)
	})

	t.Run("SecurityHeaders", func(t *testing.T) {
		w := httptest.NewRecorder()
		sm.ApplySecurityHeaders(w)

		assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "1; mode=block", w.Header().Get("X-XSS-Protection"))
		assert.Equal(t, "max-age=31536000; includeSubDomains", w.Header().Get("Strict-Transport-Security"))
	})

	t.Run("Metrics", func(t *testing.T) {
		metrics := sm.GetMetrics()
		assert.Greater(t, metrics.TotalRequests, int64(0))
		assert.GreaterOrEqual(t, metrics.BlockedRequests, int64(0))
		assert.GreaterOrEqual(t, metrics.AuthFailures, int64(0))
		assert.GreaterOrEqual(t, metrics.WAFBlocks, int64(0))
	})
}

// TestRateLimiter tests the rate limiter component
func TestRateLimiter(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := security.RateLimiterConfig{
		RPS:    10,
		Burst:  20,
		Window: time.Minute,
		Logger: logger,
	}

	rl, err := security.NewRateLimiter(config)
	require.NoError(t, err)
	require.NotNil(t, rl)

	t.Run("WithinLimit", func(t *testing.T) {
		// Process requests within rate limit
		for i := 0; i < 5; i++ {
			err := rl.CheckLimit("client1")
			assert.NoError(t, err)
		}
	})

	t.Run("ExceedLimit", func(t *testing.T) {
		// Exceed rate limit
		for i := 0; i < 25; i++ { // More than burst
			err := rl.CheckLimit("client2")
			if i < 20 {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		}
	})

	t.Run("Stats", func(t *testing.T) {
		stats := rl.GetStats()
		assert.Equal(t, 10, stats["rps"])
		assert.Equal(t, 20, stats["burst"])
		assert.GreaterOrEqual(t, stats["active_buckets"], 0)
	})
}

// TestAuthManager tests the authentication manager
func TestAuthManager(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := security.AuthManagerConfig{
		Methods:      []string{"jwt", "apikey", "basic"},
		JWTSecret:    "test-secret-key",
		APIKeyHeader: "X-API-Key",
		Logger:       logger,
	}

	am, err := security.NewAuthManager(config)
	require.NoError(t, err)
	require.NotNil(t, am)

	t.Run("JWTGeneration", func(t *testing.T) {
		user := &security.User{
			ID:       "testuser",
			Username: "testuser",
			Email:    "test@example.com",
			Roles:    []string{"user"},
		}

		token, err := am.GenerateJWT(user, time.Hour)
		assert.NoError(t, err)
		assert.NotEmpty(t, token)
	})

	t.Run("APIKeyGeneration", func(t *testing.T) {
		user := &security.User{
			ID:       "testuser",
			Username: "testuser",
			Email:    "test@example.com",
			Roles:    []string{"user"},
		}

		apiKey, err := am.GenerateAPIKey(user, "test-key")
		assert.NoError(t, err)
		assert.NotEmpty(t, apiKey.Key)
		assert.Equal(t, user.ID, apiKey.UserID)
		assert.Equal(t, "test-key", apiKey.Name)
	})

	t.Run("RoleBasedAccess", func(t *testing.T) {
		// Test user with admin role
		adminCtx := &security.SecurityContext{
			Authenticated: true,
			UserID:        "admin",
			Roles:         []string{"admin", "user"},
		}

		assert.True(t, am.HasRole(adminCtx, "admin"))
		assert.True(t, am.HasRole(adminCtx, "user"))
		assert.True(t, am.HasPermission(adminCtx, "admin"))
		assert.True(t, am.HasPermission(adminCtx, "read"))

		// Test user with only user role
		userCtx := &security.SecurityContext{
			Authenticated: true,
			UserID:        "user",
			Roles:         []string{"user"},
		}

		assert.False(t, am.HasRole(userCtx, "admin"))
		assert.True(t, am.HasRole(userCtx, "user"))
		assert.False(t, am.HasPermission(userCtx, "admin"))
		assert.True(t, am.HasPermission(userCtx, "read"))
	})
}

// TestWAFManager tests the Web Application Firewall
func TestWAFManager(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := security.WAFManagerConfig{
		Rules:  []string{"sql_injection", "xss"},
		Mode:   "block",
		Logger: logger,
	}

	wm, err := security.NewWAFManager(config)
	require.NoError(t, err)
	require.NotNil(t, wm)

	t.Run("SQLInjectionDetection", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/search?q=' OR 1=1 --", nil)
		securityCtx := &security.SecurityContext{
			ClientIP:  "192.168.1.1",
			UserAgent: "test-agent",
			RequestID: "test-123",
		}

		err := wm.CheckRequest(req, securityCtx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "WAF rule")
	})

	t.Run("XSSDetection", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/comment=<script>alert('xss')</script>", nil)
		securityCtx := &security.SecurityContext{
			ClientIP:  "192.168.1.1",
			UserAgent: "test-agent",
			RequestID: "test-123",
		}

		err := wm.CheckRequest(req, securityCtx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "WAF rule")
	})

	t.Run("CleanRequest", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/users", nil)
		securityCtx := &security.SecurityContext{
			ClientIP:  "192.168.1.1",
			UserAgent: "test-agent",
			RequestID: "test-123",
		}

		err := wm.CheckRequest(req, securityCtx)
		assert.NoError(t, err)
	})

	t.Run("Stats", func(t *testing.T) {
		stats := wm.GetStats()
		assert.Equal(t, "block", stats["mode"])
		assert.Greater(t, stats["total_rules"], 0)
		assert.Greater(t, stats["enabled_rules"], 0)
	})
}

// TestCertificateManager tests the certificate manager
func TestCertificateManager(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := security.CertificateManagerConfig{
		AutoRenewal:   false, // Disable for testing
		RotationDays:  30,
		TLSMinVersion: "1.2",
		Logger:        logger,
	}

	cm, err := security.NewCertificateManager(config)
	require.NoError(t, err)
	require.NotNil(t, cm)

	t.Run("TLSConfig", func(t *testing.T) {
		tlsConfig, err := cm.GetTLSConfig()
		assert.NoError(t, err)
		assert.NotNil(t, tlsConfig)
		assert.Equal(t, uint16(0x0303), tlsConfig.MinVersion) // TLS 1.2
		assert.NotEmpty(t, tlsConfig.Certificates)
	})

	t.Run("CertificateInfo", func(t *testing.T) {
		certInfo, err := cm.GetCertificateInfo()
		assert.NoError(t, err)
		assert.NotNil(t, certInfo)
		assert.Equal(t, "localhost", certInfo.Domain)
		assert.Greater(t, certInfo.DaysRemaining, 0)
	})

	cm.Stop()
}

// TestAuditLogger tests the audit logger
func TestAuditLogger(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := security.AuditLoggerConfig{
		LogPath:       "/tmp/test_audit_security.log",
		RetentionDays: 1, // Short retention for testing
		Logger:        logger,
	}

	al, err := security.NewAuditLogger(config)
	require.NoError(t, err)
	require.NotNil(t, al)

	t.Run("LogEvent", func(t *testing.T) {
		securityCtx := &security.SecurityContext{
			ClientIP:      "192.168.1.1",
			UserAgent:     "test-agent",
			RequestID:     "test-123",
			Authenticated: true,
			UserID:        "testuser",
			Roles:         []string{"user"},
			Blocked:       false,
			RiskScore:     0.3,
			Metadata:      map[string]interface{}{"username": "testuser"},
		}

		// Log a security event
		al.LogEvent("security_check_passed", securityCtx, nil)

		// Log a blocked event
		securityCtx.Blocked = true
		securityCtx.BlockReason = "rate_limit_exceeded"
		al.LogEvent("rate_limit_hit", securityCtx, nil)
	})

	t.Run("Stats", func(t *testing.T) {
		stats := al.GetStats()
		assert.Equal(t, "/tmp/test_audit_security.log", stats["log_path"])
		assert.Equal(t, 1, stats["retention_days"])
		assert.GreaterOrEqual(t, stats["file_size"], 0)
	})

	al.Close()
}

// TestSecurityIntegration tests the complete security integration
func TestSecurityIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create comprehensive security configuration
	config := security.SecurityConfig{
		RateLimitEnabled:   true,
		RateLimitRPS:       100,
		RateLimitBurst:     200,
		AuthEnabled:        true,
		AuthMethods:        []string{"apikey"},
		APIKeyHeader:       "X-API-Key",
		WAFEnabled:         true,
		WAFRules:           []string{"sql_injection"},
		WAFMode:            "block",
		AuditEnabled:       true,
		AuditLogPath:       "/tmp/test_integration_audit.log",
		AuditRetentionDays: 1,
		Logger:             logger,
	}

	sm, err := security.NewSecurityManager(config)
	require.NoError(t, err)
	require.NotNil(t, sm)
	defer sm.Stop()

	t.Run("CompleteSecurityFlow", func(t *testing.T) {
		// Test a request that passes all security checks
		req := httptest.NewRequest("GET", "/api/users", nil)
		req.Header.Set("X-API-Key", "test-api-key-123") // Valid test key
		req.RemoteAddr = "192.168.1.1"

		securityCtx, err := sm.ProcessRequest(context.Background(), req)
		assert.NoError(t, err)
		assert.False(t, securityCtx.Blocked)
		assert.True(t, securityCtx.Authenticated)
		assert.Equal(t, "user123", securityCtx.UserID)

		// Test a request that gets blocked by WAF
		req = httptest.NewRequest("GET", "/search", nil)
		req.URL.RawQuery = "q=' OR 1=1 --"
		req.Header.Set("X-API-Key", "test-api-key-123")
		req.RemoteAddr = "192.168.1.1"

		securityCtx, err = sm.ProcessRequest(context.Background(), req)
		assert.Error(t, err)
		assert.True(t, securityCtx.Blocked)
		assert.Equal(t, "waf_block", securityCtx.BlockReason)
	})

	t.Run("SecurityMetrics", func(t *testing.T) {
		metrics := sm.GetMetrics()
		assert.Greater(t, metrics.TotalRequests, int64(0))
		assert.Greater(t, metrics.WAFBlocks, int64(0))
		assert.GreaterOrEqual(t, metrics.AuthFailures, int64(0))
	})
}
