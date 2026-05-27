package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	name   string
	state  int32 // 0: closed, 1: open, 2: half-open
	config CircuitBreakerConfig
	mu     sync.RWMutex

	// Metrics
	requests        int64
	failures        int64
	successes       int64
	timeouts        int64
	lastFailureTime time.Time

	// Channels
	stateChangeChan chan StateChangeEvent

	// Logger
	logger *zap.Logger
}

// CircuitBreakerConfig contains circuit breaker configuration
type CircuitBreakerConfig struct {
	Name          string
	MaxRequests   int64
	Interval      time.Duration
	Timeout       time.Duration
	ReadyToTrip   func(counts Counts) bool
	OnStateChange func(name string, from State, to State)
	IsSuccessful  func(err error) bool
	Logger        *zap.Logger
}

// State represents circuit breaker state
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

// Counts holds circuit breaker counts
type Counts struct {
	Requests             int64
	TotalSuccesses       int64
	TotalFailures        int64
	ConsecutiveSuccesses int64
	ConsecutiveFailures  int64
}

// StateChangeEvent represents a state change event
type StateChangeEvent struct {
	Name   string
	From   State
	To     State
	Time   time.Time
	Reason string
}

// DefaultCircuitBreakerConfig returns default circuit breaker configuration
func DefaultCircuitBreakerConfig(name string) CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Name:        name,
		MaxRequests: 100,
		Interval:    time.Minute,
		Timeout:     60 * time.Second,
		ReadyToTrip: func(counts Counts) bool {
			return counts.ConsecutiveFailures > 5 || counts.FailureRatio() > 0.6
		},
		IsSuccessful: func(err error) bool {
			return err == nil
		},
	}
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	if config.MaxRequests <= 0 {
		config.MaxRequests = 100
	}
	if config.Interval <= 0 {
		config.Interval = time.Minute
	}
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	if config.ReadyToTrip == nil {
		config.ReadyToTrip = func(counts Counts) bool {
			return counts.ConsecutiveFailures > 5
		}
	}
	if config.IsSuccessful == nil {
		config.IsSuccessful = func(err error) bool {
			return err == nil
		}
	}

	cb := &CircuitBreaker{
		name:            config.Name,
		config:          config,
		stateChangeChan: make(chan StateChangeEvent, 10),
		logger:          config.Logger,
	}

	// Start state change monitor
	go cb.monitorStateChanges()

	return cb
}

// Execute executes the given function if the circuit breaker allows it
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	generation, err := cb.beforeRequest(ctx)
	if err != nil {
		return err
	}

	defer func() {
		e := recover()
		if e != nil {
			cb.afterRequest(ctx, generation, false)
			panic(e)
		}
	}()

	err = fn()
	cb.afterRequest(ctx, generation, cb.config.IsSuccessful(err))
	return err
}

// beforeRequest prepares the circuit breaker for a request
func (cb *CircuitBreaker) beforeRequest(ctx context.Context) (uint64, error) {
	state := cb.currentState()

	if state == StateOpen {
		return 0, ErrCircuitBreakerOpen
	}

	if state == StateHalfOpen && atomic.LoadInt64(&cb.requests) >= cb.config.MaxRequests {
		return 0, ErrTooManyRequests
	}

	atomic.AddInt64(&cb.requests, 1)
	return uint64(atomic.LoadInt64(&cb.requests)), nil
}

// afterRequest processes the result of a request
func (cb *CircuitBreaker) afterRequest(ctx context.Context, generation uint64, success bool) {
	state := cb.currentState()

	switch state {
	case StateClosed:
		if success {
			cb.onSuccess()
		} else {
			cb.onFailure()
		}
	case StateHalfOpen:
		if success {
			cb.onSuccess()
			if cb.counts().ConsecutiveSuccesses >= cb.config.MaxRequests {
				cb.setState(StateClosed, "consecutive successes threshold reached")
			}
		} else {
			cb.onFailure()
			cb.setState(StateOpen, "failure in half-open state")
		}
	}
}

// onSuccess handles successful requests
func (cb *CircuitBreaker) onSuccess() {
	atomic.AddInt64(&cb.successes, 1)
}

// onFailure handles failed requests
func (cb *CircuitBreaker) onFailure() {
	atomic.AddInt64(&cb.failures, 1)
	cb.lastFailureTime = time.Now()

	counts := cb.counts()
	if cb.config.ReadyToTrip(counts) {
		cb.setState(StateOpen, "failure threshold reached")
	}
}

// currentState returns the current state
func (cb *CircuitBreaker) currentState() State {
	return State(atomic.LoadInt32(&cb.state))
}

// setState sets the circuit breaker state
func (cb *CircuitBreaker) setState(state State, reason string) {
	oldState := cb.currentState()
	if oldState == state {
		return
	}

	atomic.StoreInt32(&cb.state, int32(state))

	cb.logger.Info("Circuit breaker state changed",
		zap.String("name", cb.name),
		zap.String("from", oldState.String()),
		zap.String("to", state.String()),
		zap.String("reason", reason))

	// Notify state change
	select {
	case cb.stateChangeChan <- StateChangeEvent{
		Name:   cb.name,
		From:   oldState,
		To:     state,
		Time:   time.Now(),
		Reason: reason,
	}:
	default:
	}

	// Call custom state change handler
	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(cb.name, oldState, state)
	}

	// Reset counters when transitioning to closed
	if state == StateClosed {
		cb.resetCounters()
	}
}

// resetCounters resets all counters
func (cb *CircuitBreaker) resetCounters() {
	atomic.StoreInt64(&cb.requests, 0)
	atomic.StoreInt64(&cb.failures, 0)
	atomic.StoreInt64(&cb.successes, 0)
	atomic.StoreInt64(&cb.timeouts, 0)
}

// counts returns current counts
func (cb *CircuitBreaker) counts() Counts {
	return Counts{
		Requests:             atomic.LoadInt64(&cb.requests),
		TotalSuccesses:       atomic.LoadInt64(&cb.successes),
		TotalFailures:        atomic.LoadInt64(&cb.failures),
		ConsecutiveSuccesses: cb.consecutiveSuccesses(),
		ConsecutiveFailures:  cb.consecutiveFailures(),
	}
}

// consecutiveSuccesses returns consecutive successes
func (cb *CircuitBreaker) consecutiveSuccesses() int64 {
	// Simplified implementation
	if atomic.LoadInt64(&cb.failures) == 0 {
		return atomic.LoadInt64(&cb.successes)
	}
	return 0
}

// consecutiveFailures returns consecutive failures
func (cb *CircuitBreaker) consecutiveFailures() int64 {
	// Simplified implementation
	if atomic.LoadInt64(&cb.successes) == 0 {
		return atomic.LoadInt64(&cb.failures)
	}
	return 0
}

// monitorStateChanges monitors state changes and handles timeouts
func (cb *CircuitBreaker) monitorStateChanges() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cb.checkStateTransitions()
		case event := <-cb.stateChangeChan:
			cb.handleStateChange(event)
		}
	}
}

// checkStateTransitions checks if state transitions are needed
func (cb *CircuitBreaker) checkStateTransitions() {
	state := cb.currentState()

	switch state {
	case StateOpen:
		if time.Since(cb.lastFailureTime) >= cb.config.Timeout {
			cb.setState(StateHalfOpen, "timeout elapsed, transitioning to half-open")
		}
	case StateClosed:
		if time.Since(cb.lastFailureTime) >= cb.config.Interval {
			cb.resetCounters()
		}
	}
}

// handleStateChange handles state change events
func (cb *CircuitBreaker) handleStateChange(event StateChangeEvent) {
	cb.logger.Info("State change event",
		zap.String("name", event.Name),
		zap.String("from", event.From.String()),
		zap.String("to", event.To.String()),
		zap.Time("time", event.Time),
		zap.String("reason", event.Reason))
}

// GetState returns the current state
func (cb *CircuitBreaker) GetState() State {
	return cb.currentState()
}

// GetStats returns circuit breaker statistics
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	counts := cb.counts()
	return CircuitBreakerStats{
		Name:                 cb.name,
		State:                cb.currentState(),
		Requests:             counts.Requests,
		Successes:            counts.TotalSuccesses,
		Failures:             counts.TotalFailures,
		ConsecutiveSuccesses: counts.ConsecutiveSuccesses,
		ConsecutiveFailures:  counts.ConsecutiveFailures,
		LastFailureTime:      cb.lastFailureTime,
	}
}

// ForceOpen forces the circuit breaker to open state
func (cb *CircuitBreaker) ForceOpen(reason string) {
	cb.setState(StateOpen, reason)
}

// ForceClose forces the circuit breaker to closed state
func (cb *CircuitBreaker) ForceClose(reason string) {
	cb.setState(StateClosed, reason)
}

// Health check for circuit breaker
func (cb *CircuitBreaker) Health() error {
	state := cb.currentState()
	if state == StateOpen {
		return fmt.Errorf("circuit breaker is open")
	}
	return nil
}

// String returns string representation of state
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// FailureRatio returns the failure ratio
func (c Counts) FailureRatio() float64 {
	if c.Requests == 0 {
		return 0
	}
	return float64(c.TotalFailures) / float64(c.Requests)
}

// CircuitBreakerStats contains circuit breaker statistics
type CircuitBreakerStats struct {
	Name                 string    `json:"name"`
	State                State     `json:"state"`
	Requests             int64     `json:"requests"`
	Successes            int64     `json:"successes"`
	Failures             int64     `json:"failures"`
	ConsecutiveSuccesses int64     `json:"consecutive_successes"`
	ConsecutiveFailures  int64     `json:"consecutive_failures"`
	LastFailureTime      time.Time `json:"last_failure_time"`
}

// CircuitBreakerManager manages multiple circuit breakers
type CircuitBreakerManager struct {
	mu              sync.RWMutex
	circuitBreakers map[string]*CircuitBreaker
	logger          *zap.Logger
}

// NewCircuitBreakerManager creates a new circuit breaker manager
func NewCircuitBreakerManager(logger *zap.Logger) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		circuitBreakers: make(map[string]*CircuitBreaker),
		logger:          logger,
	}
}

// GetCircuitBreaker gets or creates a circuit breaker
func (cbm *CircuitBreakerManager) GetCircuitBreaker(name string, config CircuitBreakerConfig) *CircuitBreaker {
	cbm.mu.Lock()
	defer cbm.mu.Unlock()

	if cb, exists := cbm.circuitBreakers[name]; exists {
		return cb
	}

	config.Name = name
	if config.Logger == nil {
		config.Logger = cbm.logger
	}

	cb := NewCircuitBreaker(config)
	cbm.circuitBreakers[name] = cb
	return cb
}

// GetAllStats returns statistics for all circuit breakers
func (cbm *CircuitBreakerManager) GetAllStats() map[string]CircuitBreakerStats {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()

	stats := make(map[string]CircuitBreakerStats)
	for name, cb := range cbm.circuitBreakers {
		stats[name] = cb.GetStats()
	}
	return stats
}

// Health check for all circuit breakers
func (cbm *CircuitBreakerManager) Health() error {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()

	for name, cb := range cbm.circuitBreakers {
		if err := cb.Health(); err != nil {
			return fmt.Errorf("circuit breaker %s: %w", name, err)
		}
	}
	return nil
}

// Errors
var (
	ErrCircuitBreakerOpen = fmt.Errorf("circuit breaker is open")
	ErrTooManyRequests    = fmt.Errorf("too many requests in half-open state")
)
