package backend

import (
	"net/url"
	"sync"
	"time"
)

type Backend interface {
	GetURL() *url.URL
	GetWeight() int
	GetMaxConnections() int
	GetHealthCheckPath() string
	GetTimeout() time.Duration
	IsHealthy() bool
	IsDraining() bool
	GetConnectionCount() int
	GetFailureCount() int
	GetLastFailure() time.Time
	GetResponseTime() time.Duration
	GetCircuitBreaker() CircuitBreaker
	SetHealthy(healthy bool)
	SetDraining(draining bool)
	IncrementConnectionCount()
	DecrementConnectionCount()
	IncrementFailureCount()
	SetResponseTime(duration time.Duration)
}

type BackendImpl struct {
	URL             *url.URL
	Weight          int
	MaxConnections  int
	HealthCheckPath string
	Timeout         time.Duration
	Healthy         bool
	Draining        bool
	ConnectionCount int
	FailureCount    int
	LastFailure     time.Time
	ResponseTime    time.Duration
	CircuitBreaker  *CircuitBreakerImpl
	mu              sync.RWMutex
}

type CircuitBreaker interface {
	Allow() bool
	RecordSuccess()
	RecordFailure()
	GetState() string
}

type CircuitBreakerImpl struct {
	State            string // "closed", "open", "half-open"
	FailureCount     int
	LastFailTime     time.Time
	SuccessCount     int
	FailureThreshold int
	RecoveryTimeout  time.Duration
	mu               sync.RWMutex
}

func NewBackend(url *url.URL, weight, maxConnections int, healthCheckPath string, timeout time.Duration) Backend {
	return &BackendImpl{
		URL:             url,
		Weight:          weight,
		MaxConnections:  maxConnections,
		HealthCheckPath: healthCheckPath,
		Timeout:         timeout,
		Healthy:         true,
		Draining:        false,
		CircuitBreaker: &CircuitBreakerImpl{
			State:            "closed",
			FailureThreshold: 5,
			RecoveryTimeout:  30 * time.Second,
		},
	}
}

func (b *BackendImpl) GetURL() *url.URL {
	return b.URL
}

func (b *BackendImpl) GetWeight() int {
	return b.Weight
}

func (b *BackendImpl) GetMaxConnections() int {
	return b.MaxConnections
}

func (b *BackendImpl) GetHealthCheckPath() string {
	return b.HealthCheckPath
}

func (b *BackendImpl) GetTimeout() time.Duration {
	return b.Timeout
}

func (b *BackendImpl) IsHealthy() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Healthy
}

func (b *BackendImpl) IsDraining() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Draining
}

func (b *BackendImpl) GetConnectionCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ConnectionCount
}

func (b *BackendImpl) GetFailureCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.FailureCount
}

func (b *BackendImpl) GetLastFailure() time.Time {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.LastFailure
}

func (b *BackendImpl) GetResponseTime() time.Duration {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ResponseTime
}

func (b *BackendImpl) GetCircuitBreaker() CircuitBreaker {
	return b.CircuitBreaker
}

func (b *BackendImpl) SetHealthy(healthy bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Healthy = healthy
}

func (b *BackendImpl) SetDraining(draining bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Draining = draining
}

func (b *BackendImpl) IncrementConnectionCount() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ConnectionCount++
}

func (b *BackendImpl) DecrementConnectionCount() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ConnectionCount--
}

func (b *BackendImpl) IncrementFailureCount() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.FailureCount++
	b.LastFailure = time.Now()
}

func (b *BackendImpl) SetResponseTime(duration time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ResponseTime = duration
}

func (cb *CircuitBreakerImpl) Allow() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.State {
	case "closed":
		return true
	case "open":
		if time.Since(cb.LastFailTime) > cb.RecoveryTimeout {
			cb.mu.RUnlock()
			cb.mu.Lock()
			cb.State = "half-open"
			cb.SuccessCount = 0
			cb.mu.Unlock()
			cb.mu.RLock()
			return true
		}
		return false
	case "half-open":
		return true
	default:
		return false
	}
}

func (cb *CircuitBreakerImpl) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.State {
	case "closed":
		cb.FailureCount = 0
	case "half-open":
		cb.SuccessCount++
		if cb.SuccessCount >= 3 {
			cb.State = "closed"
			cb.FailureCount = 0
		}
	}
}

func (cb *CircuitBreakerImpl) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.FailureCount++
	cb.LastFailTime = time.Now()

	switch cb.State {
	case "closed":
		if cb.FailureCount >= cb.FailureThreshold {
			cb.State = "open"
		}
	case "half-open":
		cb.State = "open"
	}
}

func (cb *CircuitBreakerImpl) GetState() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.State
}
