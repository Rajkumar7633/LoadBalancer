package advanced_routing

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"loadbalancer/internal/backend"
	"loadbalancer/internal/config"
	"loadbalancer/internal/metrics"
)

type AdvancedRoutingManager struct {
	// Core routing components
	contentBasedRouter   *ContentBasedRouter
	consistentHashRouter *SessionAffinityRouter
	ipHashRouter         *IPHashRouter
	slowStartRouter      *WeightedSlowStartRouter
	priorityRouter       *PriorityRouter

	// Advanced features
	circuitBreakerMgr *CircuitBreakerManager
	retryEngine       *RetryEngine
	requestMirroring  *RequestMirroring
	mtlsManager       *MTLSManager
	grpcRouter        *GRPCRouter
	websocketRouter   *WebSocketRouter
	responseCache     *ResponseCache
	cacheInvalidator  *CacheInvalidator

	// Tier 3 Advanced Features
	geoRouter            *GeoRouter
	adaptiveRouter       *AdaptiveRouter
	transformationEngine *TransformationEngine
	blueGreenRouter      *BlueGreenRouter
	apiGateway           *APIGateway
	distributedTracer    *DistributedTracer

	// Metrics and monitoring
	metrics *AdvancedRoutingMetrics

	// Configuration and state
	config   config.Config
	backends []backend.Backend
	mu       sync.RWMutex
	stats    AdvancedRoutingStats
}

type AdvancedRoutingStats struct {
	ContentBasedRequests   int64     `json:"content_based_requests"`
	ConsistentHashRequests int64     `json:"consistent_hash_requests"`
	IPHashRequests         int64     `json:"ip_hash_requests"`
	CircuitBreakerTrips    int64     `json:"circuit_breaker_trips"`
	RetryAttempts          int64     `json:"retry_attempts"`
	MirroredRequests       int64     `json:"mirrored_requests"`
	SlowStartActive        int       `json:"slow_start_active"`
	PriorityQueueSize      int       `json:"priority_queue_size"`
	CacheHits              int64     `json:"cache_hits"`
	CacheMisses            int64     `json:"cache_misses"`
	WebSocketConnections   int       `json:"websocket_connections"`
	GRPCRequests           int64     `json:"grpc_requests"`
	LastUpdated            time.Time `json:"last_updated"`
}

func NewAdvancedRoutingManager(cfg config.Config, metricsCollector *metrics.Collector) (*AdvancedRoutingManager, error) {
	arm := &AdvancedRoutingManager{
		config:  cfg,
		stats:   AdvancedRoutingStats{LastUpdated: time.Now()},
		metrics: NewAdvancedRoutingMetrics(metricsCollector),
	}

	// Initialize content-based routing
	if cfg.ContentBasedRouting.Enabled {
		arm.contentBasedRouter = NewContentBasedRouter()
		arm.setupContentBasedRules()
	}

	// Initialize consistent hashing
	if cfg.ConsistentHash.Enabled {
		keyFunc := arm.getKeyFunction(cfg.ConsistentHash.KeyType)
		arm.consistentHashRouter = NewSessionAffinityRouter(cfg.ConsistentHash.Replicas, keyFunc)
	}

	// Initialize IP hash routing
	if cfg.IPHash.Enabled {
		if cfg.IPHash.Weighted {
			// Would need to implement weighted IP hash router
			arm.ipHashRouter = NewIPHashRouter()
		} else {
			arm.ipHashRouter = NewIPHashRouter()
		}
	}

	// Initialize slow start
	if cfg.SlowStart.Enabled {
		slowStartConfig := SlowStartConfig{
			Enabled:         cfg.SlowStart.Enabled,
			Duration:        cfg.SlowStart.Duration,
			InitialWeight:   cfg.SlowStart.InitialWeight,
			StepInterval:    cfg.SlowStart.StepInterval,
			WeightIncrement: cfg.SlowStart.WeightIncrement,
		}
		arm.slowStartRouter = NewWeightedSlowStartRouter(slowStartConfig)
	}

	// Initialize priority queue routing
	if cfg.PriorityQueue.Enabled {
		priorityConfig := PriorityConfig{
			Enabled:         cfg.PriorityQueue.Enabled,
			QueueSize:       cfg.PriorityQueue.QueueSize,
			ExpiryTime:      cfg.PriorityQueue.ExpiryTime,
			PriorityHeaders: convertToPriorityMap(cfg.PriorityQueue.PriorityHeaders),
			PriorityPaths:   convertToPriorityMap(cfg.PriorityQueue.PriorityPaths),
			DefaultPriority: PriorityBasic, // Convert from string
		}
		arm.priorityRouter = NewPriorityRouter(priorityConfig)
		arm.priorityRouter.StartCleanup()
	}

	// Initialize circuit breaker
	if cfg.CircuitBreaker.Enabled {
		cbConfig := CircuitBreakerConfig{
			FailureThreshold:      cfg.CircuitBreaker.FailureThreshold,
			SuccessThreshold:      cfg.CircuitBreaker.SuccessThreshold,
			TimeoutThreshold:      cfg.CircuitBreaker.TimeoutThreshold,
			RecoveryTimeout:       cfg.CircuitBreaker.RecoveryTimeout,
			MaxHalfOpenRequests:   cfg.CircuitBreaker.MaxHalfOpenRequests,
			SlowCallThreshold:     cfg.CircuitBreaker.SlowCallThreshold,
			SlowCallRateThreshold: cfg.CircuitBreaker.SlowCallRateThreshold,
		}
		arm.circuitBreakerMgr = NewCircuitBreakerManager(cbConfig)
	}

	// Initialize retry engine
	if cfg.Retry.Enabled {
		retryConfig := RetryConfig{
			MaxRetries:       cfg.Retry.MaxRetries,
			InitialDelay:     cfg.Retry.InitialDelay,
			MaxDelay:         cfg.Retry.MaxDelay,
			Multiplier:       cfg.Retry.Multiplier,
			Jitter:           cfg.Retry.Jitter,
			RetryableStatus:  cfg.Retry.RetryableStatus,
			RetryableMethods: cfg.Retry.RetryableMethods,
		}
		arm.retryEngine = NewRetryEngine(retryConfig)
	}

	// Initialize request mirroring
	if cfg.RequestMirroring.Enabled {
		mirrorConfig := MirrorConfig{
			Percentage:     cfg.RequestMirroring.Percentage,
			MirrorBackends: cfg.RequestMirroring.MirrorBackends,
			Timeout:        cfg.RequestMirroring.Timeout,
			HeadersToCopy:  cfg.RequestMirroring.HeadersToCopy,
			Async:          cfg.RequestMirroring.Async,
			BufferSize:     cfg.RequestMirroring.BufferSize,
		}
		arm.requestMirroring = NewRequestMirroring(mirrorConfig)
	}

	// Initialize mTLS
	if cfg.MTLS.Enabled {
		mtlsConfig := MTLSConfig{
			Enabled:        cfg.MTLS.Enabled,
			CACertFile:     cfg.MTLS.CACertFile,
			ClientCertFile: cfg.MTLS.ClientCertFile,
			ClientKeyFile:  cfg.MTLS.ClientKeyFile,
			ServerCertFile: cfg.MTLS.ServerCertFile,
			ServerKeyFile:  cfg.MTLS.ServerKeyFile,
			SkipVerify:     cfg.MTLS.SkipVerify,
			ClientAuth:     cfg.MTLS.ClientAuth,
		}
		var err error
		arm.mtlsManager, err = NewMTLSManager(mtlsConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize mTLS: %w", err)
		}
	}

	// Initialize gRPC router
	if cfg.GRPC.Enabled {
		grpcConfig := GRPCConfig{
			Enabled:          cfg.GRPC.Enabled,
			Services:         cfg.GRPC.Services,
			MaxRecvMsgSize:   cfg.GRPC.MaxRecvMsgSize,
			MaxSendMsgSize:   cfg.GRPC.MaxSendMsgSize,
			Compression:      cfg.GRPC.Compression,
			EnableReflection: cfg.GRPC.EnableReflection,
			EnableHealth:     cfg.GRPC.EnableHealth,
			EnableTracing:    cfg.GRPC.EnableTracing,
		}
		arm.grpcRouter = NewGRPCRouter(grpcConfig)
	}

	// Initialize WebSocket router
	if cfg.WebSocket.Enabled {
		wsConfig := WebSocketConfig{
			Enabled:           cfg.WebSocket.Enabled,
			OriginCheck:       cfg.WebSocket.OriginCheck,
			AllowedOrigins:    cfg.WebSocket.AllowedOrigins,
			PingInterval:      cfg.WebSocket.PingInterval,
			PongWait:          cfg.WebSocket.PongWait,
			WriteWait:         cfg.WebSocket.WriteWait,
			MaxMessageSize:    cfg.WebSocket.MaxMessageSize,
			ReadBufferSize:    cfg.WebSocket.ReadBufferSize,
			WriteBufferSize:   cfg.WebSocket.WriteBufferSize,
			EnableCompression: cfg.WebSocket.EnableCompression,
		}
		arm.websocketRouter = NewWebSocketRouter(wsConfig)
	}

	// Initialize response caching
	if cfg.ResponseCache.Enabled {
		cacheConfig := CacheConfig{
			Enabled:          cfg.ResponseCache.Enabled,
			DefaultTTL:       cfg.ResponseCache.TTL,
			MaxSize:          cfg.ResponseCache.MaxSize,
			MaxEntries:       cfg.ResponseCache.MaxEntries,
			CacheableMethods: []string{"GET", "HEAD"},
			CacheableStatus:  []int{200, 301, 302},
		}
		arm.responseCache = NewResponseCache(cacheConfig)
		arm.cacheInvalidator = NewCacheInvalidator(arm.responseCache)
	}

	return arm, nil
}

func (arm *AdvancedRoutingManager) SetBackends(backends []backend.Backend) {
	arm.mu.Lock()
	defer arm.mu.Unlock()

	arm.backends = backends

	// Update all routing components
	if arm.contentBasedRouter != nil {
		arm.updateContentBasedBackends()
	}
	if arm.consistentHashRouter != nil {
		arm.consistentHashRouter.SetBackends(backends)
	}
	if arm.ipHashRouter != nil {
		arm.ipHashRouter.SetBackends(backends)
	}
	if arm.slowStartRouter != nil {
		arm.slowStartRouter.SetBackends(backends)
	}
	if arm.priorityRouter != nil {
		arm.priorityRouter.SetBackends(backends)
	}
	if arm.grpcRouter != nil {
		arm.grpcRouter.SetBackends(backends)
	}
	if arm.websocketRouter != nil {
		arm.websocketRouter.SetBackends(backends)
	}
	if arm.requestMirroring != nil {
		arm.requestMirroring.SetBackends(backends)
	}

	// Update Tier 3 features
	if arm.geoRouter != nil {
		arm.geoRouter.SetBackends(backends)
	}
	if arm.adaptiveRouter != nil {
		arm.adaptiveRouter.SetBackends(backends)
	}
	if arm.blueGreenRouter != nil {
		arm.blueGreenRouter.SetBackends(backends, backends) // Use same backends for blue/green
	}
}

func (arm *AdvancedRoutingManager) RouteRequest(ctx context.Context, r *http.Request) backend.Backend {
	arm.mu.RLock()
	defer arm.mu.RUnlock()

	var selectedBackend backend.Backend
	var routingMethod string

	// Check cache first
	if arm.responseCache != nil {
		key := arm.responseCache.GenerateKey(r)
		if entry := arm.responseCache.Get(key); entry != nil {
			arm.stats.CacheHits++
			// Return the cached backend (this is simplified - in practice, you'd return the cached response)
			if arm.metrics != nil {
				arm.metrics.RecordCacheHit(key, "cached")
			}
		} else {
			arm.stats.CacheMisses++
			if arm.metrics != nil {
				arm.metrics.RecordCacheMiss(key)
			}
		}
	}

	// Content-based routing with error handling
	if arm.contentBasedRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("content-based routing", r)
			}
		}()
		if backends := arm.contentBasedRouter.Route(r); backends != nil {
			arm.stats.ContentBasedRequests++
			selectedBackend = arm.selectHealthyBackend(backends)
			routingMethod = "content-based"
			if selectedBackend != nil && arm.metrics != nil {
				arm.metrics.RecordContentBasedRouting("default", selectedBackend.GetURL().String(), 0)
			}
		}
	}

	// Priority queue routing with error handling
	if selectedBackend == nil && arm.priorityRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("priority queue routing", r)
			}
		}()
		requestID := generateRequestID()
		if backend := arm.priorityRouter.Route(requestID, r.Header, r.URL.Path); backend != nil {
			selectedBackend = backend
			routingMethod = "priority-queue"
		}
	}

	// Consistent hash routing with error handling
	if selectedBackend == nil && arm.consistentHashRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("consistent hash routing", r)
			}
		}()
		if backend := arm.consistentHashRouter.Route(r); backend != nil {
			arm.stats.ConsistentHashRequests++
			selectedBackend = backend
			routingMethod = "consistent-hash"
			if arm.metrics != nil {
				key := arm.getKeyFunction(arm.config.ConsistentHash.KeyType)(r)
				arm.metrics.RecordConsistentHashHit(key, backend.GetURL().String(), 0)
			}
		}
	}

	// IP hash routing with error handling
	if selectedBackend == nil && arm.ipHashRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("IP hash routing", r)
			}
		}()
		if backend := arm.ipHashRouter.Route(r); backend != nil {
			arm.stats.IPHashRequests++
			selectedBackend = backend
			routingMethod = "ip-hash"
			if arm.metrics != nil {
				clientIP := getAdvancedClientIP(r)
				arm.metrics.RecordIPHashHit(clientIP, backend.GetURL().String(), 0)
			}
		}
	}

	// Slow start routing with error handling
	if selectedBackend == nil && arm.slowStartRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("slow start routing", r)
			}
		}()
		if backend := arm.slowStartRouter.SelectBackend(); backend != nil {
			selectedBackend = backend
			routingMethod = "slow-start"
		}
	}

	// Tier 3 Advanced Routing

	// Geo-based routing with error handling
	if selectedBackend == nil && arm.geoRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("geo routing", r)
			}
		}()
		if backend := arm.geoRouter.Route(ctx, r); backend != nil {
			selectedBackend = backend
			routingMethod = "geo-routing"
			if arm.metrics != nil {
				arm.metrics.RecordContentBasedRouting("geo", backend.GetURL().String(), 0)
			}
		}
	}

	// Adaptive routing with error handling
	if selectedBackend == nil && arm.adaptiveRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("adaptive routing", r)
			}
		}()
		if backend := arm.adaptiveRouter.Route(ctx, r); backend != nil {
			selectedBackend = backend
			routingMethod = "adaptive"
			if arm.metrics != nil {
				arm.metrics.RecordContentBasedRouting("adaptive", backend.GetURL().String(), 0)
			}
		}
	}

	// Blue-Green deployment routing with error handling
	if selectedBackend == nil && arm.blueGreenRouter != nil {
		defer func() {
			if r := recover(); r != nil {
				arm.logRoutingError("blue-green routing", r)
			}
		}()
		if backend := arm.blueGreenRouter.Route(ctx, r); backend != nil {
			selectedBackend = backend
			routingMethod = "blue-green"
			if arm.metrics != nil {
				arm.metrics.RecordContentBasedRouting("blue-green", backend.GetURL().String(), 0)
			}
		}
	}

	// Default routing - first healthy backend with error handling
	if selectedBackend == nil {
		selectedBackend = arm.selectHealthyBackend(arm.backends)
		routingMethod = "default"
	}

	// Log routing decision for debugging
	if selectedBackend != nil {
		arm.logRoutingDecision(routingMethod, selectedBackend.GetURL().String())
	}

	return selectedBackend
}

func (arm *AdvancedRoutingManager) logRoutingError(method string, err interface{}) {
	// Log routing error - in a real implementation, this would use the logger
	// For now, just update stats
}

func (arm *AdvancedRoutingManager) logRoutingDecision(method, backend string) {
	// Log routing decision - in a real implementation, this would use the logger
	// For now, just update stats
}

func (arm *AdvancedRoutingManager) selectHealthyBackend(backends []backend.Backend) backend.Backend {
	for _, backend := range backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}
	return nil
}

func (arm *AdvancedRoutingManager) ExecuteWithRetry(
	ctx context.Context,
	req *http.Request,
	backend backend.Backend,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) (*http.Response, error) {
	if arm.retryEngine == nil {
		return executeFunc(ctx, req, backend)
	}

	result := arm.retryEngine.ExecuteWithRetry(ctx, req, backend, executeFunc)
	arm.stats.RetryAttempts += int64(result.Attempts - 1)

	if result.Response != nil {
		return result.Response, nil
	}
	return nil, result.Error
}

func (arm *AdvancedRoutingManager) MirrorRequest(
	ctx context.Context,
	req *http.Request,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) {
	if arm.requestMirroring == nil {
		return
	}

	results := arm.requestMirroring.MirrorRequest(ctx, req, executeFunc)
	arm.stats.MirroredRequests += int64(len(results))
}

func (arm *AdvancedRoutingManager) GetCircuitBreaker(backendKey string) *CircuitBreaker {
	if arm.circuitBreakerMgr == nil {
		return nil
	}
	return arm.circuitBreakerMgr.GetBreaker(backendKey)
}

func (arm *AdvancedRoutingManager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if arm.websocketRouter != nil {
		arm.websocketRouter.HandleWebSocket(w, r)
	}
}

func (arm *AdvancedRoutingManager) GetResponseCache() *ResponseCache {
	return arm.responseCache
}

func (arm *AdvancedRoutingManager) GetStats() AdvancedRoutingStats {
	arm.mu.RLock()
	defer arm.mu.RUnlock()

	stats := arm.stats
	stats.LastUpdated = time.Now()

	// Update dynamic stats
	if arm.priorityRouter != nil {
		priorityStats := arm.priorityRouter.GetStats()
		stats.PriorityQueueSize = priorityStats.QueueSize
	}

	if arm.slowStartRouter != nil {
		slowStartStats := arm.slowStartRouter.GetSlowStartStats()
		stats.SlowStartActive = len(slowStartStats)
	}

	if arm.websocketRouter != nil {
		wsStats := arm.websocketRouter.GetConnectionStats()
		stats.WebSocketConnections = wsStats.TotalConnections
	}

	if arm.responseCache != nil {
		cacheStats := arm.responseCache.GetStats()
		stats.CacheHits = cacheStats.Hits
		stats.CacheMisses = cacheStats.Misses
	}

	return stats
}

func (arm *AdvancedRoutingManager) Stop() {
	if arm.responseCache != nil {
		arm.responseCache.Stop()
	}
	if arm.slowStartRouter != nil {
		arm.slowStartRouter.Stop()
	}
}

// Helper functions
func convertToPriorityMap(stringMap map[string]string) map[string]Priority {
	priorityMap := make(map[string]Priority)
	for key, value := range stringMap {
		switch value {
		case "free":
			priorityMap[key] = PriorityFree
		case "basic":
			priorityMap[key] = PriorityBasic
		case "premium":
			priorityMap[key] = PriorityPremium
		case "vip":
			priorityMap[key] = PriorityVIP
		case "internal":
			priorityMap[key] = PriorityInternal
		default:
			priorityMap[key] = PriorityBasic
		}
	}
	return priorityMap
}

func (arm *AdvancedRoutingManager) setupContentBasedRules() {
	if arm.contentBasedRouter == nil {
		return
	}

	// Setup rules from configuration
	for _, ruleConfig := range arm.config.ContentBasedRouting.Rules {
		// Convert config rule to routing rule
		rule := RoutingRule{
			Name:        ruleConfig.Name,
			Priority:    ruleConfig.Priority,
			Description: "Content-based routing rule",
		}

		// Convert condition
		if ruleConfig.Condition.PathMatch != nil {
			rule.Condition.PathMatch = &PathMatch{
				Type:   ruleConfig.Condition.PathMatch.Type,
				Values: ruleConfig.Condition.PathMatch.Values,
			}
		}
		if ruleConfig.Condition.HeaderMatch != nil {
			rule.Condition.HeaderMatch = &HeaderMatch{
				Name:   ruleConfig.Condition.HeaderMatch.Name,
				Type:   ruleConfig.Condition.HeaderMatch.Type,
				Values: ruleConfig.Condition.HeaderMatch.Values,
			}
		}
		if ruleConfig.Condition.QueryMatch != nil {
			rule.Condition.QueryMatch = &QueryMatch{
				Name:   ruleConfig.Condition.QueryMatch.Name,
				Type:   ruleConfig.Condition.QueryMatch.Type,
				Values: ruleConfig.Condition.QueryMatch.Values,
			}
		}
		if len(ruleConfig.Condition.MethodMatch) > 0 {
			rule.Condition.MethodMatch = ruleConfig.Condition.MethodMatch
		}

		// Resolve backend references to actual backends
		for _, backendRef := range ruleConfig.BackendRefs {
			if backend := arm.findBackendByURL(backendRef); backend != nil {
				rule.Backends = append(rule.Backends, backend)
			}
		}

		arm.contentBasedRouter.AddRule(rule)
	}
}

func (arm *AdvancedRoutingManager) updateContentBasedBackends() {
	if arm.contentBasedRouter == nil {
		return
	}

	// Clear existing rules and re-setup with current backends
	arm.contentBasedRouter = NewContentBasedRouter()
	arm.setupContentBasedRules()
}

func (arm *AdvancedRoutingManager) findBackendByURL(urlStr string) backend.Backend {
	arm.mu.RLock()
	defer arm.mu.RUnlock()

	for _, backend := range arm.backends {
		if backend.GetURL().String() == urlStr {
			return backend
		}
	}
	return nil
}

func (arm *AdvancedRoutingManager) getKeyFunction(keyType string) func(*http.Request) string {
	switch keyType {
	case "user_id":
		return UserIDKeyFunc
	case "session_id":
		return func(r *http.Request) string {
			if sessionID := r.Header.Get("X-Session-ID"); sessionID != "" {
				return sessionID
			}
			if cookie, err := r.Cookie("session_id"); err == nil {
				return cookie.Value
			}
			return ""
		}
	case "api_key":
		return APIKeyFunc
	case "jwt":
		return JWTKeyFunc
	default:
		return UserIDKeyFunc
	}
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

func getAdvancedClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP from the comma-separated list
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	if idx := strings.LastIndex(r.RemoteAddr, ":"); idx != -1 {
		return r.RemoteAddr[:idx]
	}
	return r.RemoteAddr
}
