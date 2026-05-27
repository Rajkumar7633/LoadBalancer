package loadbalancer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"loadbalancer/internal/advanced_routing"
	"loadbalancer/internal/backend"
	"loadbalancer/internal/config"
	"loadbalancer/internal/healthcheck"
	"loadbalancer/internal/logger"
	"loadbalancer/internal/metrics"
	"loadbalancer/internal/pool"
	"loadbalancer/internal/ratelimit"
	"loadbalancer/internal/routing"
)

type LoadBalancer struct {
	config            *config.Config
	logger            *logger.Logger
	metrics           *metrics.Collector
	backends          []backend.Backend
	routingEngine     routing.Engine
	healthChecker     *healthcheck.Checker
	rateLimiter       *ratelimit.Limiter
	connectionPool    *pool.Pool
	advancedRouting   *advanced_routing.AdvancedRoutingManager
	mu                sync.RWMutex
	shutdownChan      chan struct{}
	activeConnections int
}

func New(cfg *config.Config, log *logger.Logger, metrics *metrics.Collector) (*LoadBalancer, error) {
	lb := &LoadBalancer{
		config:       cfg,
		logger:       log,
		metrics:      metrics,
		backends:     make([]backend.Backend, 0),
		shutdownChan: make(chan struct{}),
	}

	// Initialize backends
	for _, backendCfg := range cfg.Backends {
		parsedURL, err := url.Parse(backendCfg.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid backend URL %s: %w", backendCfg.URL, err)
		}

		backend := backend.NewBackend(
			parsedURL,
			backendCfg.Weight,
			backendCfg.MaxConnections,
			backendCfg.HealthCheckPath,
			backendCfg.Timeout,
		)
		lb.backends = append(lb.backends, backend)
	}

	// Initialize routing engine
	routingEngine, err := routing.NewEngine(cfg.Routing.Algorithm, lb.backends, cfg.Routing)
	if err != nil {
		return nil, fmt.Errorf("failed to create routing engine: %w", err)
	}
	lb.routingEngine = routingEngine

	// Initialize advanced routing manager
	advancedRouting, err := advanced_routing.NewAdvancedRoutingManager(*cfg, metrics)
	if err != nil {
		return nil, fmt.Errorf("failed to create advanced routing manager: %w", err)
	}
	lb.advancedRouting = advancedRouting

	// Initialize health checker
	lb.healthChecker = healthcheck.NewChecker(lb.backends, cfg.HealthCheck, lb.logger, lb.metrics)

	// Initialize rate limiter
	lb.rateLimiter = ratelimit.NewLimiter(cfg.RateLimit, lb.logger, lb.metrics)

	// Initialize connection pool
	lb.connectionPool = pool.NewPool(cfg.Pool, lb.logger)

	// Set backends in advanced routing manager
	lb.advancedRouting.SetBackends(lb.backends)

	return lb, nil
}

func (lb *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// Rate limiting
	if !lb.rateLimiter.Allow(r) {
		lb.logger.Warn("Request rate limited", "client_ip", getClientIP(r))
		lb.metrics.RecordRateLimitHit(getClientIP(r), "global")
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	// Select backend using advanced routing
	backend := lb.advancedRouting.RouteRequest(r.Context(), r)
	if backend == nil {
		// Fallback to basic routing if advanced routing fails
		backend = lb.routingEngine.SelectBackend(r)
	}
	if backend == nil {
		lb.logger.Warn("No healthy backends available")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	// Check circuit breaker
	circuitBreaker := lb.advancedRouting.GetCircuitBreaker(backend.GetURL().String())
	if circuitBreaker != nil && !circuitBreaker.Allow() {
		lb.logger.Warn("Circuit breaker open", "backend", backend.GetURL().String())
		lb.metrics.RecordCircuitBreakerTrip(backend.GetURL().String(), "circuit_open")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	// Check connection limits
	if backend.GetConnectionCount() >= backend.GetMaxConnections() {
		lb.logger.Warn("Backend connection limit reached", "backend", backend.GetURL().String())
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	// Increment active connections
	lb.mu.Lock()
	lb.activeConnections++
	backend.IncrementConnectionCount()
	lb.mu.Unlock()

	defer func() {
		lb.mu.Lock()
		lb.activeConnections--
		backend.DecrementConnectionCount()
		lb.mu.Unlock()
		lb.metrics.SetActiveConnections(lb.activeConnections)
	}()

	// Proxy request to backend with retry and mirroring
	lb.proxyRequestWithAdvancedFeatures(w, r, backend, startTime)
}

func (lb *LoadBalancer) proxyRequestWithAdvancedFeatures(w http.ResponseWriter, r *http.Request, backend backend.Backend, startTime time.Time) {
	// Check for WebSocket upgrade
	if r.Header.Get("Upgrade") == "websocket" {
		lb.advancedRouting.HandleWebSocket(w, r)
		return
	}

	// Check cache first
	if cache := lb.advancedRouting.GetResponseCache(); cache != nil {
		key := cache.GenerateKey(r)
		if entry := cache.Get(key); entry != nil {
			// Serve cached response
			for name, value := range entry.Headers {
				w.Header().Set(name, value)
			}
			w.WriteHeader(entry.StatusCode)
			w.Write(entry.Body)
			lb.logger.Debug("Cache hit", "key", key)
			return
		}
	}

	// Mirror request if enabled
	lb.advancedRouting.MirrorRequest(r.Context(), r, lb.executeBackendRequest)

	// Execute request with retry
	resp, err := lb.advancedRouting.ExecuteWithRetry(r.Context(), r, backend, lb.executeBackendRequest)
	if err != nil {
		lb.logger.Error("Backend request failed after retries", "backend", backend.GetURL().String(), "error", err)

		// Record circuit breaker failure
		if circuitBreaker := lb.advancedRouting.GetCircuitBreaker(backend.GetURL().String()); circuitBreaker != nil {
			circuitBreaker.RecordFailure(0)
		}

		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Record circuit breaker success
	if circuitBreaker := lb.advancedRouting.GetCircuitBreaker(backend.GetURL().String()); circuitBreaker != nil {
		circuitBreaker.RecordSuccess(time.Since(startTime))
	}

	// Cache response if applicable
	if cache := lb.advancedRouting.GetResponseCache(); cache != nil {
		key := cache.GenerateKey(r)
		body, _ := io.ReadAll(resp.Body)
		cache.Set(key, resp, body)
		resp.Body = io.NopCloser(bytes.NewBuffer(body))
	}

	// Copy response headers
	for k, v := range resp.Header {
		w.Header()[k] = v
	}

	// Set response status
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	io.Copy(w, resp.Body)

	// Record metrics
	duration := time.Since(startTime)
	lb.metrics.RecordRequest(r.Method, r.URL.Path, fmt.Sprintf("%d", resp.StatusCode), backend.GetURL().String(), duration)
	lb.metrics.RecordRequestSize(r.Method, r.URL.Path, int(r.ContentLength))
	lb.metrics.RecordResponseSize(r.Method, r.URL.Path, fmt.Sprintf("%d", resp.StatusCode), int(resp.ContentLength))

	// Log access
	lb.logger.Info("Request completed",
		"method", r.Method,
		"path", r.URL.Path,
		"status", resp.StatusCode,
		"backend", backend.GetURL().String(),
		"duration", duration,
		"client_ip", getClientIP(r),
	)
}

func (lb *LoadBalancer) executeBackendRequest(ctx context.Context, r *http.Request, backend backend.Backend) (*http.Response, error) {
	// Create proxy request
	proxyReq := r.Clone(ctx)
	proxyReq.URL.Scheme = backend.GetURL().Scheme
	proxyReq.URL.Host = backend.GetURL().Host
	proxyReq.URL.Path = backend.GetURL().Path + r.URL.Path
	proxyReq.RequestURI = ""

	// Remove hop-by-hop headers
	removeHopByHopHeaders(proxyReq.Header)

	// Use connection pool
	client := lb.connectionPool.GetClient()
	defer lb.connectionPool.PutClient(client)

	// Set timeout
	ctx, cancel := context.WithTimeout(r.Context(), backend.GetTimeout())
	defer cancel()
	proxyReq = proxyReq.WithContext(ctx)

	// Make request
	resp, err := client.Do(proxyReq)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (lb *LoadBalancer) StartHealthChecks(ctx context.Context) {
	lb.healthChecker.Start(ctx)
}

func (lb *LoadBalancer) Shutdown() {
	close(lb.shutdownChan)
	lb.connectionPool.Close()
	lb.healthChecker.Stop()

	// Shutdown advanced routing components
	if lb.advancedRouting != nil {
		lb.advancedRouting.Stop()
	}
}

func (lb *LoadBalancer) GetBackends() []backend.Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.backends
}

func removeHopByHopHeaders(header http.Header) {
	// Remove hop-by-hop headers
	hopByHopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	for _, h := range hopByHopHeaders {
		header.Del(h)
	}

	// Remove Connection header values
	if connections := header["Connection"]; len(connections) > 0 {
		for _, c := range connections {
			header.Del(c)
		}
	}
}

func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	return r.RemoteAddr
}
