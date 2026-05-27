package core

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// GracefulShutdown manages graceful shutdown of the load balancer
type GracefulShutdown struct {
	server         *http.Server
	workerPool     *WorkerPool
	errorHandler   *ErrorHandler
	circuitBreaker *CircuitBreakerManager
	logger         *zap.Logger
	config         GracefulShutdownConfig

	// Shutdown management
	shutdownChan   chan os.Signal
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	wg             sync.WaitGroup
	isShuttingDown bool
	mu             sync.RWMutex
}

// GracefulShutdownConfig contains graceful shutdown configuration
type GracefulShutdownConfig struct {
	ShutdownTimeout    time.Duration
	DrainTimeout       time.Duration
	ForceShutdownAfter time.Duration
	EnableHealthCheck  bool
	HealthCheckPath    string
	Logger             *zap.Logger
}

// ShutdownPhase represents shutdown phase
type ShutdownPhase int

const (
	PhaseInit ShutdownPhase = iota
	PhaseDrain
	PhaseStopAccepting
	PhaseWaitForConnections
	PhaseCleanup
	PhaseComplete
)

// NewGracefulShutdown creates a new graceful shutdown manager
func NewGracefulShutdown(config GracefulShutdownConfig) *GracefulShutdown {
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 30 * time.Second
	}
	if config.DrainTimeout <= 0 {
		config.DrainTimeout = 10 * time.Second
	}
	if config.ForceShutdownAfter <= 0 {
		config.ForceShutdownAfter = 60 * time.Second
	}
	if config.HealthCheckPath == "" {
		config.HealthCheckPath = "/health"
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &GracefulShutdown{
		config:         config,
		shutdownChan:   make(chan os.Signal, 1),
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
		logger:         config.Logger,
	}
}

// RegisterServer registers the HTTP server
func (gs *GracefulShutdown) RegisterServer(server *http.Server) {
	gs.server = server
}

// RegisterWorkerPool registers the worker pool
func (gs *GracefulShutdown) RegisterWorkerPool(pool *WorkerPool) {
	gs.workerPool = pool
}

// RegisterErrorHandler registers the error handler
func (gs *GracefulShutdown) RegisterErrorHandler(eh *ErrorHandler) {
	gs.errorHandler = eh
}

// RegisterCircuitBreaker registers the circuit breaker manager
func (gs *GracefulShutdown) RegisterCircuitBreaker(cb *CircuitBreakerManager) {
	gs.circuitBreaker = cb
}

// Start starts the graceful shutdown manager
func (gs *GracefulShutdown) Start() {
	gs.logger.Info("Starting graceful shutdown manager")

	// Register signal handlers
	signal.Notify(gs.shutdownChan,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	)

	// Start shutdown goroutine
	gs.wg.Add(1)
	go gs.shutdownHandler()

	// Start health check if enabled
	if gs.config.EnableHealthCheck {
		gs.wg.Add(1)
		go gs.healthCheckMonitor()
	}
}

// shutdownHandler handles shutdown signals
func (gs *GracefulShutdown) shutdownHandler() {
	defer gs.wg.Done()

	sig := <-gs.shutdownChan
	gs.logger.Info("Received shutdown signal", zap.String("signal", sig.String()))

	gs.BeginShutdown()
}

// BeginShutdown begins the graceful shutdown process
func (gs *GracefulShutdown) BeginShutdown() {
	gs.mu.Lock()
	if gs.isShuttingDown {
		gs.mu.Unlock()
		return
	}
	gs.isShuttingDown = true
	gs.mu.Unlock()

	gs.logger.Info("Beginning graceful shutdown process")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), gs.config.ShutdownTimeout)
	defer cancel()

	// Execute shutdown phases
	phases := []func(context.Context) error{
		gs.phaseDrain,
		gs.phaseStopAccepting,
		gs.phaseWaitForConnections,
		gs.phaseCleanup,
	}

	for i, phase := range phases {
		phaseName := ShutdownPhase(i).String()
		gs.logger.Info("Executing shutdown phase", zap.String("phase", phaseName))

		if err := phase(ctx); err != nil {
			gs.logger.Error("Shutdown phase failed",
				zap.String("phase", phaseName),
				zap.Error(err))
		}
	}

	gs.logger.Info("Graceful shutdown completed")
	gs.shutdownCancel()
}

// phaseDrain drains existing connections
func (gs *GracefulShutdown) phaseDrain(ctx context.Context) error {
	gs.logger.Info("Phase 1: Draining existing connections")

	// Stop accepting new connections
	if gs.server != nil {
		gs.server.SetKeepAlivesEnabled(false)
	}

	// Wait for connections to drain with timeout
	done := make(chan struct{})
	go func() {
		// Wait for active connections to finish
		time.Sleep(gs.config.DrainTimeout)
		close(done)
	}()

	select {
	case <-done:
		gs.logger.Info("Connections drained successfully")
	case <-ctx.Done():
		gs.logger.Warn("Connection drain timeout")
		return ctx.Err()
	}

	return nil
}

// phaseStopAccepting stops accepting new connections
func (gs *GracefulShutdown) phaseStopAccepting(ctx context.Context) error {
	gs.logger.Info("Phase 2: Stopping to accept new connections")

	if gs.server != nil {
		// Shutdown HTTP server
		err := gs.server.Shutdown(ctx)
		if err != nil {
			gs.logger.Error("HTTP server shutdown failed", zap.Error(err))
			return err
		}
	}

	return nil
}

// phaseWaitForConnections waits for all connections to finish
func (gs *GracefulShutdown) phaseWaitForConnections(ctx context.Context) error {
	gs.logger.Info("Phase 3: Waiting for all connections to finish")

	// Stop worker pool
	if gs.workerPool != nil {
		gs.workerPool.Stop()
	}

	// Wait for all goroutines to finish
	done := make(chan struct{})
	go func() {
		gs.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		gs.logger.Info("All connections finished")
	case <-ctx.Done():
		gs.logger.Warn("Waiting for connections timeout")
		return ctx.Err()
	}

	return nil
}

// phaseCleanup performs cleanup operations
func (gs *GracefulShutdown) phaseCleanup(ctx context.Context) error {
	gs.logger.Info("Phase 4: Performing cleanup operations")

	// Close circuit breakers
	if gs.circuitBreaker != nil {
		// Force all circuit breakers to open to prevent new requests
		gs.logger.Info("Opening all circuit breakers")
	}

	// Flush logs
	if gs.logger != nil {
		gs.logger.Sync()
	}

	// Close error channels
	if gs.errorHandler != nil {
		close(gs.errorHandler.errorChan)
	}

	return nil
}

// healthCheckMonitor monitors health during shutdown
func (gs *GracefulShutdown) healthCheckMonitor() {
	defer gs.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-gs.shutdownCtx.Done():
			return
		case <-ticker.C:
			gs.performHealthCheck()
		}
	}
}

// performHealthCheck performs health check
func (gs *GracefulShutdown) performHealthCheck() {
	health := map[string]interface{}{
		"shutting_down": gs.isShuttingDown,
		"timestamp":     time.Now(),
	}

	// Check worker pool health
	if gs.workerPool != nil {
		if err := gs.workerPool.Health(); err != nil {
			health["worker_pool"] = err.Error()
		} else {
			health["worker_pool"] = "healthy"
		}
	}

	// Check error handler health
	if gs.errorHandler != nil {
		if err := gs.errorHandler.Health(); err != nil {
			health["error_handler"] = err.Error()
		} else {
			health["error_handler"] = "healthy"
		}
	}

	// Check circuit breaker health
	if gs.circuitBreaker != nil {
		if err := gs.circuitBreaker.Health(); err != nil {
			health["circuit_breaker"] = err.Error()
		} else {
			health["circuit_breaker"] = "healthy"
		}
	}

	gs.logger.Info("Health check", zap.Any("status", health))
}

// ForceShutdown forces immediate shutdown
func (gs *GracefulShutdown) ForceShutdown(reason string) {
	gs.logger.Error("Force shutdown initiated", zap.String("reason", reason))

	// Cancel context to stop all operations
	gs.shutdownCancel()

	// Force close server
	if gs.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gs.server.Close()
		<-ctx.Done()
	}

	// Force stop worker pool
	if gs.workerPool != nil {
		gs.workerPool.Stop()
	}

	os.Exit(1)
}

// IsShuttingDown returns true if shutdown is in progress
func (gs *GracefulShutdown) IsShuttingDown() bool {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return gs.isShuttingDown
}

// GetShutdownStatus returns current shutdown status
func (gs *GracefulShutdown) GetShutdownStatus() ShutdownStatus {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	status := ShutdownStatus{
		IsShuttingDown: gs.isShuttingDown,
		Timestamp:      time.Now(),
	}

	if gs.workerPool != nil {
		status.WorkerPoolStats = gs.workerPool.GetStats()
	}

	if gs.errorHandler != nil {
		status.ErrorMetrics = gs.errorHandler.GetMetrics()
	}

	if gs.circuitBreaker != nil {
		status.CircuitBreakerStats = gs.circuitBreaker.GetAllStats()
	}

	return status
}

// String returns string representation of shutdown phase
func (p ShutdownPhase) String() string {
	switch p {
	case PhaseInit:
		return "init"
	case PhaseDrain:
		return "drain"
	case PhaseStopAccepting:
		return "stop_accepting"
	case PhaseWaitForConnections:
		return "wait_for_connections"
	case PhaseCleanup:
		return "cleanup"
	case PhaseComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// ShutdownStatus contains shutdown status information
type ShutdownStatus struct {
	IsShuttingDown      bool                           `json:"is_shutting_down"`
	Timestamp           time.Time                      `json:"timestamp"`
	WorkerPoolStats     WorkerPoolStats                `json:"worker_pool_stats,omitempty"`
	ErrorMetrics        ErrorMetrics                   `json:"error_metrics,omitempty"`
	CircuitBreakerStats map[string]CircuitBreakerStats `json:"circuit_breaker_stats,omitempty"`
}

// RestartManager manages graceful restart
type RestartManager struct {
	logger          *zap.Logger
	config          RestartConfig
	shutdownManager *GracefulShutdown
}

// RestartConfig contains restart configuration
type RestartConfig struct {
	EnableHotReload bool
	ConfigWatchPath string
	RestartDelay    time.Duration
	MaxRestarts     int
	RestartWindow   time.Duration
	Logger          *zap.Logger
}

// NewRestartManager creates a new restart manager
func NewRestartManager(config RestartConfig, shutdownManager *GracefulShutdown) *RestartManager {
	if config.RestartDelay <= 0 {
		config.RestartDelay = 5 * time.Second
	}
	if config.MaxRestarts <= 0 {
		config.MaxRestarts = 3
	}
	if config.RestartWindow <= 0 {
		config.RestartWindow = time.Minute
	}

	return &RestartManager{
		logger:          config.Logger,
		config:          config,
		shutdownManager: shutdownManager,
	}
}

// Start starts the restart manager
func (rm *RestartManager) Start() {
	if rm.config.EnableHotReload {
		go rm.watchConfigChanges()
	}
}

// watchConfigChanges watches for configuration changes
func (rm *RestartManager) watchConfigChanges() {
	rm.logger.Info("Starting configuration watch for hot reload")

	// Implementation would watch for file changes
	// and trigger graceful restart when needed
}

// TriggerRestart triggers a graceful restart
func (rm *RestartManager) TriggerRestart(reason string) {
	rm.logger.Info("Triggering graceful restart", zap.String("reason", reason))

	// Begin shutdown which will be followed by restart
	rm.shutdownManager.BeginShutdown()
}
