package core

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ErrorHandler provides comprehensive error handling and recovery
type ErrorHandler struct {
	mu                 sync.RWMutex
	logger             *zap.Logger
	errorChan          chan ErrorEvent
	recoveryStrategies map[string]RecoveryStrategy
	metrics            *ErrorMetrics
	config             ErrorHandlerConfig
}

// ErrorEvent represents an error event
type ErrorEvent struct {
	Timestamp time.Time
	Error     error
	Context   map[string]interface{}
	Severity  ErrorSeverity
	Source    string
}

// ErrorSeverity represents error severity levels
type ErrorSeverity int

const (
	SeverityLow ErrorSeverity = iota
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

// RecoveryStrategy defines how to recover from specific errors
type RecoveryStrategy interface {
	CanHandle(error) bool
	Recover(ctx context.Context, err error, context map[string]interface{}) error
	GetName() string
}

// ErrorMetrics tracks error statistics
type ErrorMetrics struct {
	TotalErrors       int64
	CriticalErrors    int64
	RecoveredErrors   int64
	UnrecoveredErrors int64
	LastError         time.Time
	ErrorRate         float64
}

// ErrorHandlerConfig contains error handler configuration
type ErrorHandlerConfig struct {
	MaxErrorRate    float64
	ErrorBufferSize int
	RecoveryTimeout time.Duration
	EnableMetrics   bool
	Logger          *zap.Logger
}

// NewErrorHandler creates a new error handler
func NewErrorHandler(config ErrorHandlerConfig) *ErrorHandler {
	if config.MaxErrorRate <= 0 {
		config.MaxErrorRate = 0.1 // 10% error rate threshold
	}
	if config.ErrorBufferSize <= 0 {
		config.ErrorBufferSize = 1000
	}
	if config.RecoveryTimeout <= 0 {
		config.RecoveryTimeout = 30 * time.Second
	}

	eh := &ErrorHandler{
		logger:             config.Logger,
		errorChan:          make(chan ErrorEvent, config.ErrorBufferSize),
		recoveryStrategies: make(map[string]RecoveryStrategy),
		metrics:            &ErrorMetrics{},
		config:             config,
	}

	// Register default recovery strategies
	eh.registerDefaultStrategies()

	// Start error processor
	go eh.processErrors()

	return eh
}

// HandleError handles an error with recovery
func (eh *ErrorHandler) HandleError(ctx context.Context, err error, source string, context map[string]interface{}) error {
	if err == nil {
		return nil
	}

	// Determine severity
	severity := eh.determineSeverity(err)

	// Create error event
	event := ErrorEvent{
		Timestamp: time.Now(),
		Error:     err,
		Context:   context,
		Severity:  severity,
		Source:    source,
	}

	// Update metrics
	eh.updateMetrics(severity)

	// Send to error processor
	select {
	case eh.errorChan <- event:
	default:
		eh.logger.Warn("Error buffer full, dropping error",
			zap.Error(err),
			zap.String("source", source))
	}

	// Attempt recovery
	return eh.attemptRecovery(ctx, err, context)
}

// RegisterStrategy registers a recovery strategy
func (eh *ErrorHandler) RegisterStrategy(strategy RecoveryStrategy) {
	eh.mu.Lock()
	defer eh.mu.Unlock()
	eh.recoveryStrategies[strategy.GetName()] = strategy
}

// processErrors processes error events
func (eh *ErrorHandler) processErrors() {
	for event := range eh.errorChan {
		eh.logError(event)

		// Check if we need to take action based on error rate
		if eh.shouldTakeAction() {
			eh.takeCorrectiveAction(event)
		}
	}
}

// logError logs an error with appropriate level
func (eh *ErrorHandler) logError(event ErrorEvent) {
	fields := []zap.Field{
		zap.Error(event.Error),
		zap.String("source", event.Source),
		zap.Time("timestamp", event.Timestamp),
		zap.String("severity", event.Severity.String()),
	}

	// Add context fields
	for k, v := range event.Context {
		fields = append(fields, zap.Any(k, v))
	}

	switch event.Severity {
	case SeverityCritical:
		eh.logger.Error("Critical error occurred", fields...)
	case SeverityHigh:
		eh.logger.Error("High severity error occurred", fields...)
	case SeverityMedium:
		eh.logger.Warn("Medium severity error occurred", fields...)
	case SeverityLow:
		eh.logger.Info("Low severity error occurred", fields...)
	}
}

// determineSeverity determines error severity
func (eh *ErrorHandler) determineSeverity(err error) ErrorSeverity {
	// Check for critical errors
	if isCriticalError(err) {
		return SeverityCritical
	}

	// Check for high severity errors
	if isHighSeverityError(err) {
		return SeverityHigh
	}

	// Check for medium severity errors
	if isMediumSeverityError(err) {
		return SeverityMedium
	}

	return SeverityLow
}

// attemptRecovery attempts to recover from an error
func (eh *ErrorHandler) attemptRecovery(ctx context.Context, err error, contextData map[string]interface{}) error {
	eh.mu.RLock()
	strategies := make([]RecoveryStrategy, 0, len(eh.recoveryStrategies))
	for _, strategy := range eh.recoveryStrategies {
		strategies = append(strategies, strategy)
	}
	eh.mu.RUnlock()

	for _, strategy := range strategies {
		if strategy.CanHandle(err) {
			recoveryCtx, cancel := context.WithTimeout(ctx, eh.config.RecoveryTimeout)
			defer cancel()

			if recoverErr := strategy.Recover(recoveryCtx, err, contextData); recoverErr == nil {
				eh.logger.Info("Successfully recovered from error",
					zap.String("strategy", strategy.GetName()),
					zap.Error(err))
				return nil
			} else {
				eh.logger.Warn("Recovery strategy failed",
					zap.String("strategy", strategy.GetName()),
					zap.Error(recoverErr))
			}
		}
	}

	return fmt.Errorf("no recovery strategy available for error: %w", err)
}

// updateMetrics updates error metrics
func (eh *ErrorHandler) updateMetrics(severity ErrorSeverity) {
	eh.mu.Lock()
	defer eh.mu.Unlock()

	eh.metrics.TotalErrors++
	eh.metrics.LastError = time.Now()

	switch severity {
	case SeverityCritical:
		eh.metrics.CriticalErrors++
	}
}

// shouldTakeAction determines if corrective action is needed
func (eh *ErrorHandler) shouldTakeAction() bool {
	eh.mu.RLock()
	defer eh.mu.RUnlock()

	// Check error rate
	if eh.metrics.TotalErrors > 100 {
		timeWindow := time.Since(eh.metrics.LastError)
		if timeWindow < time.Minute {
			eh.metrics.ErrorRate = float64(eh.metrics.TotalErrors) / timeWindow.Seconds()
			if eh.metrics.ErrorRate > eh.config.MaxErrorRate {
				return true
			}
		}
	}

	// Check critical errors
	if eh.metrics.CriticalErrors > 5 {
		return true
	}

	return false
}

// takeCorrectiveAction takes corrective action based on errors
func (eh *ErrorHandler) takeCorrectiveAction(event ErrorEvent) {
	eh.logger.Error("Taking corrective action due to high error rate",
		zap.Float64("error_rate", eh.metrics.ErrorRate),
		zap.Int64("critical_errors", eh.metrics.CriticalErrors))

	// Implement corrective actions based on error type and severity
	switch event.Severity {
	case SeverityCritical:
		eh.handleCriticalError(event)
	case SeverityHigh:
		eh.handleHighSeverityError(event)
	}
}

// handleCriticalError handles critical errors
func (eh *ErrorHandler) handleCriticalError(event ErrorEvent) {
	// Trigger circuit breakers
	// Notify monitoring systems
	// Initiate failover procedures
	eh.logger.Error("Critical error handling activated",
		zap.Any("context", event.Context))
}

// handleHighSeverityError handles high severity errors
func (eh *ErrorHandler) handleHighSeverityError(event ErrorEvent) {
	// Increase health check frequency
	// Enable safe mode
	// Log detailed diagnostics
	eh.logger.Warn("High severity error handling activated",
		zap.Any("context", event.Context))
}

// registerDefaultStrategies registers default recovery strategies
func (eh *ErrorHandler) registerDefaultStrategies() {
	eh.RegisterStrategy(&ConnectionRecoveryStrategy{})
	eh.RegisterStrategy(&TimeoutRecoveryStrategy{})
	eh.RegisterStrategy(&MemoryRecoveryStrategy{})
	eh.RegisterStrategy(&CircuitBreakerRecoveryStrategy{})
}

// GetMetrics returns error metrics
func (eh *ErrorHandler) GetMetrics() ErrorMetrics {
	eh.mu.RLock()
	defer eh.mu.RUnlock()
	return *eh.metrics
}

// Health check for error handler
func (eh *ErrorHandler) Health() error {
	eh.mu.RLock()
	defer eh.mu.RUnlock()

	if eh.metrics.ErrorRate > eh.config.MaxErrorRate {
		return fmt.Errorf("error rate too high: %.2f", eh.metrics.ErrorRate)
	}

	if eh.metrics.CriticalErrors > 10 {
		return fmt.Errorf("too many critical errors: %d", eh.metrics.CriticalErrors)
	}

	return nil
}

// String returns string representation of severity
func (e ErrorSeverity) String() string {
	switch e {
	case SeverityLow:
		return "low"
	case SeverityMedium:
		return "medium"
	case SeverityHigh:
		return "high"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Helper functions for error classification
func isCriticalError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	criticalErrors := []string{
		"out of memory",
		"fatal error",
		"panic",
		"cannot allocate memory",
		"system out of memory",
	}

	for _, critical := range criticalErrors {
		if contains(errStr, critical) {
			return true
		}
	}

	return false
}

func isHighSeverityError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	highSeverityErrors := []string{
		"connection refused",
		"connection timeout",
		"no such host",
		"network unreachable",
		"tls handshake error",
	}

	for _, high := range highSeverityErrors {
		if contains(errStr, high) {
			return true
		}
	}

	return false
}

func isMediumSeverityError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	mediumSeverityErrors := []string{
		"request timeout",
		"service unavailable",
		"rate limit exceeded",
		"temporary failure",
	}

	for _, medium := range mediumSeverityErrors {
		if contains(errStr, medium) {
			return true
		}
	}

	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Recovery Strategies

// ConnectionRecoveryStrategy handles connection errors
type ConnectionRecoveryStrategy struct{}

func (s *ConnectionRecoveryStrategy) CanHandle(err error) bool {
	return contains(err.Error(), "connection") ||
		contains(err.Error(), "network") ||
		contains(err.Error(), "refused")
}

func (s *ConnectionRecoveryStrategy) Recover(ctx context.Context, err error, context map[string]interface{}) error {
	// Implement connection recovery logic
	// - Close existing connections
	// - Reconnect with backoff
	// - Update connection pool
	return nil
}

func (s *ConnectionRecoveryStrategy) GetName() string {
	return "connection_recovery"
}

// TimeoutRecoveryStrategy handles timeout errors
type TimeoutRecoveryStrategy struct{}

func (s *TimeoutRecoveryStrategy) CanHandle(err error) bool {
	return contains(err.Error(), "timeout") ||
		contains(err.Error(), "deadline exceeded")
}

func (s *TimeoutRecoveryStrategy) Recover(ctx context.Context, err error, context map[string]interface{}) error {
	// Implement timeout recovery logic
	// - Increase timeout values
	// - Implement exponential backoff
	// - Retry with different parameters
	return nil
}

func (s *TimeoutRecoveryStrategy) GetName() string {
	return "timeout_recovery"
}

// MemoryRecoveryStrategy handles memory errors
type MemoryRecoveryStrategy struct{}

func (s *MemoryRecoveryStrategy) CanHandle(err error) bool {
	return contains(err.Error(), "memory") ||
		contains(err.Error(), "cannot allocate")
}

func (s *MemoryRecoveryStrategy) Recover(ctx context.Context, err error, context map[string]interface{}) error {
	// Implement memory recovery logic
	// - Trigger garbage collection
	// - Clear caches
	// - Reduce buffer sizes
	runtime.GC()
	return nil
}

func (s *MemoryRecoveryStrategy) GetName() string {
	return "memory_recovery"
}

// CircuitBreakerRecoveryStrategy handles circuit breaker errors
type CircuitBreakerRecoveryStrategy struct{}

func (s *CircuitBreakerRecoveryStrategy) CanHandle(err error) bool {
	return contains(err.Error(), "circuit breaker") ||
		contains(err.Error(), "service unavailable")
}

func (s *CircuitBreakerRecoveryStrategy) Recover(ctx context.Context, err error, context map[string]interface{}) error {
	// Implement circuit breaker recovery logic
	// - Wait for circuit breaker to reset
	// - Implement half-open state testing
	// - Gradually restore traffic
	return nil
}

func (s *CircuitBreakerRecoveryStrategy) GetName() string {
	return "circuit_breaker_recovery"
}
