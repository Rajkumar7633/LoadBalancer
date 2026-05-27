package security

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"go.uber.org/zap"
)

// AuthManager handles authentication and authorization
type AuthManager struct {
	config AuthManagerConfig
	logger *zap.Logger
}

// AuthManagerConfig contains authentication configuration
type AuthManagerConfig struct {
	Methods            []string    `json:"methods"`
	JWTSecret          string      `json:"jwt_secret"`
	APIKeyHeader       string      `json:"api_key_header"`
	OAuth2Provider     string      `json:"oauth2_provider"`
	OAuth2ClientID     string      `json:"oauth2_client_id"`
	OAuth2ClientSecret string      `json:"oauth2_client_secret"`
	Logger             *zap.Logger `json:"-"`
}

// User represents an authenticated user
type User struct {
	ID       string                 `json:"id"`
	Username string                 `json:"username"`
	Email    string                 `json:"email"`
	Roles    []string               `json:"roles"`
	Metadata map[string]interface{} `json:"metadata"`
}

// JWTClaims represents JWT claims
type JWTClaims struct {
	UserID   string   `json:"user_id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

// APIKey represents an API key
type APIKey struct {
	Key     string    `json:"key"`
	UserID  string    `json:"user_id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Scopes  []string  `json:"scopes"`
}

// NewAuthManager creates a new authentication manager
func NewAuthManager(config AuthManagerConfig) (*AuthManager, error) {
	am := &AuthManager{
		config: config,
		logger: config.Logger,
	}

	// Validate configuration
	if len(config.Methods) == 0 {
		return nil, fmt.Errorf("at least one authentication method must be specified")
	}

	// Initialize authentication methods
	for _, method := range config.Methods {
		switch method {
		case "jwt":
			if config.JWTSecret == "" {
				return nil, fmt.Errorf("JWT secret is required for JWT authentication")
			}
		case "apikey":
			if config.APIKeyHeader == "" {
				config.APIKeyHeader = "X-API-Key" // Default header
			}
		case "oauth2":
			if config.OAuth2Provider == "" || config.OAuth2ClientID == "" {
				return nil, fmt.Errorf("OAuth2 provider and client ID are required for OAuth2 authentication")
			}
		}
	}

	return am, nil
}

// Authenticate authenticates a request
func (am *AuthManager) Authenticate(req *http.Request, securityCtx *SecurityContext) error {
	// Try each authentication method
	for _, method := range am.config.Methods {
		switch method {
		case "jwt":
			if err := am.authenticateJWT(req, securityCtx); err == nil {
				return nil
			}
		case "apikey":
			if err := am.authenticateAPIKey(req, securityCtx); err == nil {
				return nil
			}
		case "oauth2":
			if err := am.authenticateOAuth2(req, securityCtx); err == nil {
				return nil
			}
		case "basic":
			if err := am.authenticateBasic(req, securityCtx); err == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("authentication failed for all methods")
}

// authenticateJWT authenticates using JWT token
func (am *AuthManager) authenticateJWT(req *http.Request, securityCtx *SecurityContext) error {
	// Get token from Authorization header
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}

	// Extract Bearer token
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return fmt.Errorf("invalid Authorization header format")
	}

	tokenString := parts[1]

	// Parse and validate token
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(am.config.JWTSecret), nil
	})

	if err != nil {
		return fmt.Errorf("failed to parse JWT token: %w", err)
	}

	if !token.Valid {
		return fmt.Errorf("invalid JWT token")
	}

	// Extract claims
	claims, ok := token.Claims.(*JWTClaims)
	if !ok {
		return fmt.Errorf("invalid JWT claims")
	}

	// Check expiration
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return fmt.Errorf("JWT token has expired")
	}

	// Set security context
	securityCtx.Authenticated = true
	securityCtx.UserID = claims.UserID
	securityCtx.Roles = claims.Roles
	securityCtx.Metadata["username"] = claims.Username
	securityCtx.Metadata["auth_method"] = "jwt"

	return nil
}

// authenticateAPIKey authenticates using API key
func (am *AuthManager) authenticateAPIKey(req *http.Request, securityCtx *SecurityContext) error {
	// Get API key from header
	apiKey := req.Header.Get(am.config.APIKeyHeader)
	if apiKey == "" {
		return fmt.Errorf("missing API key header")
	}

	// Validate API key (in production, this would check against a database)
	user, err := am.validateAPIKey(apiKey)
	if err != nil {
		return fmt.Errorf("invalid API key: %w", err)
	}

	// Set security context
	securityCtx.Authenticated = true
	securityCtx.UserID = user.ID
	securityCtx.Roles = user.Roles
	securityCtx.Metadata["username"] = user.Username
	securityCtx.Metadata["auth_method"] = "apikey"

	return nil
}

// authenticateOAuth2 authenticates using OAuth2
func (am *AuthManager) authenticateOAuth2(req *http.Request, securityCtx *SecurityContext) error {
	// Get OAuth2 token from Authorization header
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		return fmt.Errorf("missing Authorization header")
	}

	// Extract Bearer token
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return fmt.Errorf("invalid Authorization header format")
	}

	tokenString := parts[1]

	// Validate OAuth2 token (in production, this would validate with the OAuth2 provider)
	user, err := am.validateOAuth2Token(tokenString)
	if err != nil {
		return fmt.Errorf("invalid OAuth2 token: %w", err)
	}

	// Set security context
	securityCtx.Authenticated = true
	securityCtx.UserID = user.ID
	securityCtx.Roles = user.Roles
	securityCtx.Metadata["username"] = user.Username
	securityCtx.Metadata["auth_method"] = "oauth2"

	return nil
}

// authenticateBasic authenticates using Basic authentication
func (am *AuthManager) authenticateBasic(req *http.Request, securityCtx *SecurityContext) error {
	// Get Basic auth credentials
	username, password, ok := req.BasicAuth()
	if !ok {
		return fmt.Errorf("missing Basic authentication credentials")
	}

	// Validate credentials (in production, this would check against a database)
	user, err := am.validateBasicAuth(username, password)
	if err != nil {
		return fmt.Errorf("invalid Basic authentication credentials: %w", err)
	}

	// Set security context
	securityCtx.Authenticated = true
	securityCtx.UserID = user.ID
	securityCtx.Roles = user.Roles
	securityCtx.Metadata["username"] = user.Username
	securityCtx.Metadata["auth_method"] = "basic"

	return nil
}

// validateAPIKey validates an API key
func (am *AuthManager) validateAPIKey(apiKey string) (*User, error) {
	// In production, this would validate against a database
	// For now, we'll use a simple validation

	// Example API keys for testing
	testAPIKeys := map[string]*User{
		"test-api-key-123": {
			ID:       "user123",
			Username: "testuser",
			Email:    "test@example.com",
			Roles:    []string{"user"},
		},
		"admin-api-key-456": {
			ID:       "admin456",
			Username: "admin",
			Email:    "admin@example.com",
			Roles:    []string{"admin", "user"},
		},
	}

	user, exists := testAPIKeys[apiKey]
	if !exists {
		return nil, fmt.Errorf("API key not found")
	}

	return user, nil
}

// validateOAuth2Token validates an OAuth2 token
func (am *AuthManager) validateOAuth2Token(token string) (*User, error) {
	// In production, this would validate with the OAuth2 provider
	// For now, we'll use a simple validation

	// Example OAuth2 tokens for testing
	testTokens := map[string]*User{
		"oauth2-test-token": {
			ID:       "user789",
			Username: "oauthuser",
			Email:    "oauth@example.com",
			Roles:    []string{"user"},
		},
	}

	user, exists := testTokens[token]
	if !exists {
		return nil, fmt.Errorf("OAuth2 token not found")
	}

	return user, nil
}

// validateBasicAuth validates Basic authentication credentials
func (am *AuthManager) validateBasicAuth(username, password string) (*User, error) {
	// In production, this would validate against a database
	// For now, we'll use a simple validation

	// Example users for testing
	testUsers := map[string]string{
		"testuser": "testpass",
		"admin":    "adminpass",
	}

	expectedPassword, exists := testUsers[username]
	if !exists || expectedPassword != password {
		return nil, fmt.Errorf("invalid username or password")
	}

	roles := []string{"user"}
	if username == "admin" {
		roles = []string{"admin", "user"}
	}

	return &User{
		ID:       username,
		Username: username,
		Email:    username + "@example.com",
		Roles:    roles,
	}, nil
}

// GenerateJWT generates a JWT token for a user
func (am *AuthManager) GenerateJWT(user *User, duration time.Duration) (string, error) {
	if am.config.JWTSecret == "" {
		return "", fmt.Errorf("JWT secret not configured")
	}

	claims := JWTClaims{
		UserID:   user.ID,
		Username: user.Username,
		Roles:    user.Roles,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(duration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(am.config.JWTSecret))
}

// GenerateAPIKey generates an API key
func (am *AuthManager) GenerateAPIKey(user *User, name string) (*APIKey, error) {
	// Generate random key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate API key: %w", err)
	}

	apiKey := base64.StdEncoding.EncodeToString(key)

	return &APIKey{
		Key:     apiKey,
		UserID:  user.ID,
		Name:    name,
		Created: time.Now(),
		Expires: time.Now().Add(365 * 24 * time.Hour), // 1 year
		Scopes:  []string{"read", "write"},
	}, nil
}

// HasRole checks if a user has a specific role
func (am *AuthManager) HasRole(securityCtx *SecurityContext, role string) bool {
	if !securityCtx.Authenticated {
		return false
	}

	for _, userRole := range securityCtx.Roles {
		if userRole == role {
			return true
		}
	}

	return false
}

// HasPermission checks if a user has a specific permission
func (am *AuthManager) HasPermission(securityCtx *SecurityContext, permission string) bool {
	if !securityCtx.Authenticated {
		return false
	}

	// Simple role-based permission mapping
	// In production, this would be more sophisticated
	permissions := map[string][]string{
		"admin": {"read", "write", "delete", "admin"},
		"user":  {"read", "write"},
		"guest": {"read"},
	}

	for _, role := range securityCtx.Roles {
		if rolePerms, exists := permissions[role]; exists {
			for _, perm := range rolePerms {
				if perm == permission {
					return true
				}
			}
		}
	}

	return false
}
