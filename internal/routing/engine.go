package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"loadbalancer/internal/backend"
	"loadbalancer/internal/config"
)

type Engine interface {
	SelectBackend(r *http.Request) backend.Backend
	UpdateBackends(backends []backend.Backend)
}

type EngineFactory struct{}

func (f *EngineFactory) CreateEngine(algorithm string, backends []backend.Backend, cfg config.RoutingConfig) (Engine, error) {
	switch algorithm {
	case "round_robin":
		return NewRoundRobinEngine(backends, cfg), nil
	case "weighted":
		return NewWeightedEngine(backends, cfg), nil
	case "least_connections":
		return NewLeastConnectionsEngine(backends, cfg), nil
	default:
		return nil, fmt.Errorf("unsupported routing algorithm: %s", algorithm)
	}
}

func NewEngine(algorithm string, backends []backend.Backend, cfg config.RoutingConfig) (Engine, error) {
	factory := &EngineFactory{}
	return factory.CreateEngine(algorithm, backends, cfg)
}

// Round Robin Engine
type RoundRobinEngine struct {
	backends        []backend.Backend
	currentIndex    int
	stickySessions  bool
	cookieName      string
	adaptiveRouting bool
	mu              sync.RWMutex
}

func NewRoundRobinEngine(backends []backend.Backend, cfg config.RoutingConfig) *RoundRobinEngine {
	return &RoundRobinEngine{
		backends:        backends,
		currentIndex:    0,
		stickySessions:  cfg.StickySessions,
		cookieName:      cfg.CookieName,
		adaptiveRouting: cfg.AdaptiveRouting,
	}
}

func (e *RoundRobinEngine) SelectBackend(r *http.Request) backend.Backend {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check for sticky sessions
	if e.stickySessions {
		if cookie, err := r.Cookie(e.cookieName); err == nil {
			if backend := e.findBackendBySession(cookie.Value); backend != nil {
				return backend
			}
		}
	}

	// Adaptive routing - consider response times
	if e.adaptiveRouting {
		return e.selectAdaptiveBackend()
	}

	// Standard round-robin
	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	backend := healthyBackends[e.currentIndex%len(healthyBackends)]
	e.currentIndex++
	return backend
}

func (e *RoundRobinEngine) selectAdaptiveBackend() backend.Backend {
	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	// Sort by response time (faster first)
	sort.Slice(healthyBackends, func(i, j int) bool {
		return healthyBackends[i].GetResponseTime() < healthyBackends[j].GetResponseTime()
	})

	// Use weighted selection based on response times
	return e.weightedSelection(healthyBackends)
}

func (e *RoundRobinEngine) weightedSelection(backends []backend.Backend) backend.Backend {
	if len(backends) == 1 {
		return backends[0]
	}

	// Calculate inverse weights (faster = higher weight)
	weights := make([]float64, len(backends))
	totalWeight := 0.0

	for i, backend := range backends {
		// Inverse of response time with minimum weight
		responseTime := backend.GetResponseTime()
		if responseTime == 0 {
			responseTime = 1 * time.Millisecond
		}
		weight := 1000.0 / responseTime.Seconds()
		weights[i] = weight
		totalWeight += weight
	}

	// Random weighted selection
	random := float64(time.Now().UnixNano()) / float64(1<<63-1) * totalWeight
	accumulated := 0.0

	for i, weight := range weights {
		accumulated += weight
		if random <= accumulated {
			return backends[i]
		}
	}

	return backends[0]
}

func (e *RoundRobinEngine) findBackendBySession(sessionID string) backend.Backend {
	// Hash session ID to get consistent backend
	hash := sha256.Sum256([]byte(sessionID))
	hashStr := hex.EncodeToString(hash[:])

	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	// Use hash to select backend consistently
	index := int(hashStr[0]) % len(healthyBackends)
	return healthyBackends[index]
}

func (e *RoundRobinEngine) getHealthyBackends() []backend.Backend {
	var healthy []backend.Backend
	for _, backend := range e.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			healthy = append(healthy, backend)
		}
	}
	return healthy
}

func (e *RoundRobinEngine) UpdateBackends(backends []backend.Backend) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.backends = backends
}

// Weighted Engine
type WeightedEngine struct {
	backends       []backend.Backend
	stickySessions bool
	cookieName     string
	currentWeight  int
	maxWeight      int
	mu             sync.RWMutex
}

func NewWeightedEngine(backends []backend.Backend, cfg config.RoutingConfig) *WeightedEngine {
	maxWeight := 0
	for _, backend := range backends {
		if backend.GetWeight() > maxWeight {
			maxWeight = backend.GetWeight()
		}
	}

	return &WeightedEngine{
		backends:       backends,
		stickySessions: cfg.StickySessions,
		cookieName:     cfg.CookieName,
		currentWeight:  0,
		maxWeight:      maxWeight,
	}
}

func (e *WeightedEngine) SelectBackend(r *http.Request) backend.Backend {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check for sticky sessions
	if e.stickySessions {
		if cookie, err := r.Cookie(e.cookieName); err == nil {
			if backend := e.findBackendBySession(cookie.Value); backend != nil {
				return backend
			}
		}
	}

	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	// Weighted round-robin algorithm
	e.currentWeight++
	if e.currentWeight > e.maxWeight {
		e.currentWeight = 1
	}

	for _, backend := range healthyBackends {
		if backend.GetWeight() >= e.currentWeight {
			return backend
		}
	}

	// Fallback to first healthy backend
	return healthyBackends[0]
}

func (e *WeightedEngine) findBackendBySession(sessionID string) backend.Backend {
	hash := sha256.Sum256([]byte(sessionID))
	hashStr := hex.EncodeToString(hash[:])

	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	index := int(hashStr[0]) % len(healthyBackends)
	return healthyBackends[index]
}

func (e *WeightedEngine) getHealthyBackends() []backend.Backend {
	var healthy []backend.Backend
	for _, backend := range e.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			healthy = append(healthy, backend)
		}
	}
	return healthy
}

func (e *WeightedEngine) UpdateBackends(backends []backend.Backend) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.backends = backends
	e.maxWeight = 0
	for _, backend := range backends {
		if backend.GetWeight() > e.maxWeight {
			e.maxWeight = backend.GetWeight()
		}
	}
}

// Least Connections Engine
type LeastConnectionsEngine struct {
	backends       []backend.Backend
	stickySessions bool
	cookieName     string
	mu             sync.RWMutex
}

func NewLeastConnectionsEngine(backends []backend.Backend, cfg config.RoutingConfig) *LeastConnectionsEngine {
	return &LeastConnectionsEngine{
		backends:       backends,
		stickySessions: cfg.StickySessions,
		cookieName:     cfg.CookieName,
	}
}

func (e *LeastConnectionsEngine) SelectBackend(r *http.Request) backend.Backend {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check for sticky sessions
	if e.stickySessions {
		if cookie, err := r.Cookie(e.cookieName); err == nil {
			if backend := e.findBackendBySession(cookie.Value); backend != nil {
				return backend
			}
		}
	}

	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	// Find backend with least connections
	var selected backend.Backend
	minConnections := int(^uint(0) >> 1) // Max int

	for _, backend := range healthyBackends {
		connections := backend.GetConnectionCount()

		if connections < minConnections {
			minConnections = connections
			selected = backend
		}
	}

	return selected
}

func (e *LeastConnectionsEngine) findBackendBySession(sessionID string) backend.Backend {
	hash := sha256.Sum256([]byte(sessionID))
	hashStr := hex.EncodeToString(hash[:])

	healthyBackends := e.getHealthyBackends()
	if len(healthyBackends) == 0 {
		return nil
	}

	index := int(hashStr[0]) % len(healthyBackends)
	return healthyBackends[index]
}

func (e *LeastConnectionsEngine) getHealthyBackends() []backend.Backend {
	var healthy []backend.Backend
	for _, backend := range e.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			healthy = append(healthy, backend)
		}
	}
	return healthy
}

func (e *LeastConnectionsEngine) UpdateBackends(backends []backend.Backend) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.backends = backends
}
