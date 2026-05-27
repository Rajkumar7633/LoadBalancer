package advanced_routing

import (
	"sync"
	"time"
)

type CircuitBreakerState string

const (
	StateClosed   CircuitBreakerState = "closed"
	StateOpen     CircuitBreakerState = "open"
	StateHalfOpen CircuitBreakerState = "half-open"
)

type CircuitBreakerConfig struct {
	FailureThreshold      int           `json:"failure_threshold"`
	SuccessThreshold      int           `json:"success_threshold"`
	TimeoutThreshold      time.Duration `json:"timeout_threshold"`
	RecoveryTimeout       time.Duration `json:"recovery_timeout"`
	MaxHalfOpenRequests   int           `json:"max_half_open_requests"`
	SlowCallThreshold     time.Duration `json:"slow_call_threshold"`
	SlowCallRateThreshold float64       `json:"slow_call_rate_threshold"`
}

type CircuitBreaker struct {
	config           CircuitBreakerConfig
	state            CircuitBreakerState
	failureCount     int
	successCount     int
	halfOpenRequests int
	lastFailureTime  time.Time
	lastStateChange  time.Time
	slowCalls        int
	totalCalls       int
	mu               sync.RWMutex
}

func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config:           config,
		state:            StateClosed,
		failureCount:     0,
		successCount:     0,
		halfOpenRequests: 0,
		lastFailureTime:  time.Time{},
		lastStateChange:  time.Now(),
		slowCalls:        0,
		totalCalls:       0,
	}
}

func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(cb.lastFailureTime) >= cb.config.RecoveryTimeout {
			cb.transitionToHalfOpen()
			return true
		}
		return false
	case StateHalfOpen:
		if cb.halfOpenRequests >= cb.config.MaxHalfOpenRequests {
			return false
		}
		cb.halfOpenRequests++
		return true
	}
	return false
}

func (cb *CircuitBreaker) RecordSuccess(duration time.Duration) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.totalCalls++

	// Check if it was a slow call
	if duration > cb.config.SlowCallThreshold {
		cb.slowCalls++
	}

	switch cb.state {
	case StateClosed:
		cb.successCount = 0
		cb.failureCount = 0
	case StateHalfOpen:
		cb.successCount++
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.transitionToClosed()
		}
	}
}

func (cb *CircuitBreaker) RecordFailure(duration time.Duration) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.totalCalls++
	cb.failureCount++
	cb.lastFailureTime = time.Now()

	// Check if it was a slow call
	if duration > cb.config.SlowCallThreshold {
		cb.slowCalls++
	}

	switch cb.state {
	case StateClosed:
		if cb.shouldTrip() {
			cb.transitionToOpen()
		}
	case StateHalfOpen:
		cb.transitionToOpen()
	}
}

func (cb *CircuitBreaker) shouldTrip() bool {
	// Trip on failure threshold
	if cb.failureCount >= cb.config.FailureThreshold {
		return true
	}

	// Trip on slow call rate threshold
	if cb.totalCalls >= 10 { // Minimum calls before considering slow call rate
		slowCallRate := float64(cb.slowCalls) / float64(cb.totalCalls)
		if slowCallRate >= cb.config.SlowCallRateThreshold {
			return true
		}
	}

	return false
}

func (cb *CircuitBreaker) transitionToOpen() {
	cb.state = StateOpen
	cb.lastStateChange = time.Now()
	cb.halfOpenRequests = 0
}

func (cb *CircuitBreaker) transitionToHalfOpen() {
	cb.state = StateHalfOpen
	cb.lastStateChange = time.Now()
	cb.successCount = 0
	cb.halfOpenRequests = 0
}

func (cb *CircuitBreaker) transitionToClosed() {
	cb.state = StateClosed
	cb.lastStateChange = time.Now()
	cb.failureCount = 0
	cb.successCount = 0
	cb.slowCalls = 0
	cb.totalCalls = 0
}

func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	slowCallRate := float64(0)
	if cb.totalCalls > 0 {
		slowCallRate = float64(cb.slowCalls) / float64(cb.totalCalls)
	}

	return CircuitBreakerStats{
		State:           cb.state,
		FailureCount:    cb.failureCount,
		SuccessCount:    cb.successCount,
		TotalCalls:      cb.totalCalls,
		SlowCalls:       cb.slowCalls,
		SlowCallRate:    slowCallRate,
		LastFailureTime: cb.lastFailureTime,
		LastStateChange: cb.lastStateChange,
	}
}

type CircuitBreakerStats struct {
	State           CircuitBreakerState `json:"state"`
	FailureCount    int                 `json:"failure_count"`
	SuccessCount    int                 `json:"success_count"`
	TotalCalls      int                 `json:"total_calls"`
	SlowCalls       int                 `json:"slow_calls"`
	SlowCallRate    float64             `json:"slow_call_rate"`
	LastFailureTime time.Time           `json:"last_failure_time"`
	LastStateChange time.Time           `json:"last_state_change"`
}

// CircuitBreakerManager manages multiple circuit breakers
type CircuitBreakerManager struct {
	breakers map[string]*CircuitBreaker
	config   CircuitBreakerConfig
	mu       sync.RWMutex
}

func NewCircuitBreakerManager(config CircuitBreakerConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
	}
}

func (cbm *CircuitBreakerManager) GetBreaker(key string) *CircuitBreaker {
	cbm.mu.Lock()
	defer cbm.mu.Unlock()

	if breaker, exists := cbm.breakers[key]; exists {
		return breaker
	}

	breaker := NewCircuitBreaker(cbm.config)
	cbm.breakers[key] = breaker
	return breaker
}

func (cbm *CircuitBreakerManager) RemoveBreaker(key string) {
	cbm.mu.Lock()
	defer cbm.mu.Unlock()
	delete(cbm.breakers, key)
}

func (cbm *CircuitBreakerManager) GetAllStats() map[string]CircuitBreakerStats {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()

	stats := make(map[string]CircuitBreakerStats)
	for key, breaker := range cbm.breakers {
		stats[key] = breaker.GetStats()
	}
	return stats
}
