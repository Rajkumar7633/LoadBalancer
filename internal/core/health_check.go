package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
)

// HealthCheckService provides comprehensive health check endpoints
type HealthCheckService struct {
	checks    map[string]HealthCheck
	config    HealthCheckConfig
	logger    *zap.Logger
	server    *http.Server
	mu        sync.RWMutex
	startTime time.Time
}

// HealthCheckConfig contains health check configuration
type HealthCheckConfig struct {
	Enabled        bool          `json:"enabled"`
	Port           int           `json:"port"`
	Path           string        `json:"path"`
	CheckInterval  time.Duration `json:"check_interval"`
	Timeout        time.Duration `json:"timeout"`
	DetailedChecks bool          `json:"detailed_checks"`
	Logger         *zap.Logger   `json:"-"`
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Name      string                 `json:"name"`
	Status    string                 `json:"status"`
	Message   string                 `json:"message"`
	Duration  time.Duration          `json:"duration"`
	Timestamp time.Time              `json:"timestamp"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

// HealthResponse represents the overall health response
type HealthResponse struct {
	Status    string                       `json:"status"`
	Timestamp time.Time                    `json:"timestamp"`
	Uptime    time.Duration                `json:"uptime"`
	Version   string                       `json:"version"`
	Checks    map[string]HealthCheckResult `json:"checks,omitempty"`
	System    SystemInfo                   `json:"system"`
}

// SystemInfo contains system information
type SystemInfo struct {
	GoVersion    string     `json:"go_version"`
	OS           string     `json:"os"`
	Arch         string     `json:"arch"`
	NumCPU       int        `json:"num_cpu"`
	NumGoroutine int        `json:"num_goroutine"`
	MemoryUsage  MemoryInfo `json:"memory_usage"`
}

// MemoryInfo contains memory information
type MemoryInfo struct {
	Alloc      uint64 `json:"alloc"`
	TotalAlloc uint64 `json:"total_alloc"`
	Sys        uint64 `json:"sys"`
	NumGC      uint32 `json:"num_gc"`
}

// NewHealthCheckService creates a new health check service
func NewHealthCheckService(config HealthCheckConfig) *HealthCheckService {
	if config.Port <= 0 {
		config.Port = 8081
	}
	if config.Path == "" {
		config.Path = "/health"
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = 30 * time.Second
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}

	return &HealthCheckService{
		checks:    make(map[string]HealthCheck),
		config:    config,
		logger:    config.Logger,
		startTime: time.Now(),
	}
}

// Start starts the health check service
func (hcs *HealthCheckService) Start(ctx context.Context) error {
	if !hcs.config.Enabled {
		hcs.logger.Info("Health check service disabled")
		return nil
	}

	hcs.logger.Info("Starting health check service",
		zap.Int("port", hcs.config.Port),
		zap.String("path", hcs.config.Path))

	// Register default health checks
	hcs.registerDefaultChecks()

	// Setup HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc(hcs.config.Path, hcs.handleHealth)
	mux.HandleFunc(hcs.config.Path+"/live", hcs.handleLiveness)
	mux.HandleFunc(hcs.config.Path+"/ready", hcs.handleReadiness)
	mux.HandleFunc(hcs.config.Path+"/detailed", hcs.handleDetailedHealth)

	hcs.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", hcs.config.Port),
		Handler:      mux,
		ReadTimeout:  hcs.config.Timeout,
		WriteTimeout: hcs.config.Timeout,
	}

	// Start server
	go func() {
		if err := hcs.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			hcs.logger.Error("Health check server failed", zap.Error(err))
		}
	}()

	// Start periodic health checks
	go hcs.runPeriodicChecks(ctx)

	return nil
}

// Stop stops the health check service
func (hcs *HealthCheckService) Stop(ctx context.Context) error {
	if hcs.server == nil {
		return nil
	}

	hcs.logger.Info("Stopping health check service")

	return hcs.server.Shutdown(ctx)
}

// AddCheck adds a health check
func (hcs *HealthCheckService) AddCheck(check HealthCheck) {
	hcs.mu.Lock()
	defer hcs.mu.Unlock()
	hcs.checks[check.Name()] = check
}

// RemoveCheck removes a health check
func (hcs *HealthCheckService) RemoveCheck(name string) {
	hcs.mu.Lock()
	defer hcs.mu.Unlock()
	delete(hcs.checks, name)
}

// registerDefaultChecks registers default health checks
func (hcs *HealthCheckService) registerDefaultChecks() {
	// Memory check
	hcs.AddCheck(&MemoryHealthCheck{
		name:   "memory",
		logger: hcs.logger,
	})

	// Goroutine check
	hcs.AddCheck(&GoroutineHealthCheck{
		name:          "goroutines",
		logger:        hcs.logger,
		maxGoroutines: 1000,
	})

	// CPU check
	hcs.AddCheck(&CPUHealthCheck{
		name:        "cpu",
		logger:      hcs.logger,
		maxCPUUsage: 90.0,
	})
}

// runPeriodicChecks runs periodic health checks
func (hcs *HealthCheckService) runPeriodicChecks(ctx context.Context) {
	ticker := time.NewTicker(hcs.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hcs.performAllChecks(ctx)
		}
	}
}

// performAllChecks performs all health checks
func (hcs *HealthCheckService) performAllChecks(ctx context.Context) {
	hcs.mu.RLock()
	checks := make(map[string]HealthCheck)
	for name, check := range hcs.checks {
		checks[name] = check
	}
	hcs.mu.RUnlock()

	for name, check := range checks {
		go func(name string, check HealthCheck) {
			start := time.Now()
			status := check.Check(ctx)
			duration := time.Since(start)

			if status.Status != "healthy" {
				hcs.logger.Warn("Health check failed",
					zap.String("check", name),
					zap.String("status", status.Status),
					zap.String("message", status.Message),
					zap.Duration("duration", duration))
			}
		}(name, check)
	}
}

// handleHealth handles basic health check requests
func (hcs *HealthCheckService) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	response := hcs.createHealthResponse(ctx, false)

	w.Header().Set("Content-Type", "application/json")

	if response.Status == "healthy" {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(response)
}

// handleLiveness handles liveness probe requests
func (hcs *HealthCheckService) handleLiveness(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"status":    "alive",
		"timestamp": time.Now(),
		"uptime":    time.Since(hcs.startTime),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// handleReadiness handles readiness probe requests
func (hcs *HealthCheckService) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	response := hcs.createHealthResponse(ctx, false)

	// For readiness, we only check critical services
	criticalChecks := map[string]HealthCheckResult{}
	for name, result := range response.Checks {
		if hcs.isCriticalCheck(name) {
			criticalChecks[name] = result
		}
	}

	readinessResponse := map[string]interface{}{
		"status":    "ready",
		"timestamp": time.Now(),
		"uptime":    time.Since(hcs.startTime),
		"checks":    criticalChecks,
	}

	// Check if all critical checks are healthy
	allHealthy := true
	for _, result := range criticalChecks {
		if result.Status != "healthy" {
			allHealthy = false
			break
		}
	}

	if !allHealthy {
		readinessResponse["status"] = "not_ready"
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(readinessResponse)
}

// handleDetailedHealth handles detailed health check requests
func (hcs *HealthCheckService) handleDetailedHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	response := hcs.createHealthResponse(ctx, true)

	w.Header().Set("Content-Type", "application/json")

	if response.Status == "healthy" {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(response)
}

// createHealthResponse creates a health response
func (hcs *HealthCheckService) createHealthResponse(ctx context.Context, detailed bool) HealthResponse {
	hcs.mu.RLock()
	checks := make(map[string]HealthCheck)
	for name, check := range hcs.checks {
		checks[name] = check
	}
	hcs.mu.RUnlock()

	results := make(map[string]HealthCheckResult)
	overallStatus := "healthy"

	for name, check := range checks {
		start := time.Now()
		status := check.Check(ctx)
		duration := time.Since(start)

		result := HealthCheckResult{
			Name:      name,
			Status:    status.Status,
			Message:   status.Message,
			Duration:  duration,
			Timestamp: status.Time,
		}

		if detailed {
			result.Details = status.Details
		}

		results[name] = result

		if status.Status != "healthy" {
			overallStatus = "unhealthy"
		}
	}

	response := HealthResponse{
		Status:    overallStatus,
		Timestamp: time.Now(),
		Uptime:    time.Since(hcs.startTime),
		Version:   "1.0.0",
		System:    hcs.getSystemInfo(),
	}

	if detailed || hcs.config.DetailedChecks {
		response.Checks = results
	}

	return response
}

// getSystemInfo gets system information
func (hcs *HealthCheckService) getSystemInfo() SystemInfo {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return SystemInfo{
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
		MemoryUsage: MemoryInfo{
			Alloc:      memStats.Alloc,
			TotalAlloc: memStats.TotalAlloc,
			Sys:        memStats.Sys,
			NumGC:      memStats.NumGC,
		},
	}
}

// isCriticalCheck determines if a check is critical
func (hcs *HealthCheckService) isCriticalCheck(name string) bool {
	criticalChecks := []string{"memory", "cpu", "goroutines"}
	for _, critical := range criticalChecks {
		if name == critical {
			return true
		}
	}
	return false
}

// Default health check implementations

// MemoryHealthCheck checks memory usage
type MemoryHealthCheck struct {
	name   string
	logger *zap.Logger
}

func (m *MemoryHealthCheck) Name() string {
	return m.name
}

func (m *MemoryHealthCheck) Check(ctx context.Context) HealthStatus {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Check if memory usage is too high (> 1GB)
	maxMemory := uint64(1024 * 1024 * 1024)
	if memStats.Alloc > maxMemory {
		return HealthStatus{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Memory usage too high: %d bytes", memStats.Alloc),
			Details: map[string]interface{}{
				"alloc":       memStats.Alloc,
				"total_alloc": memStats.TotalAlloc,
				"sys":         memStats.Sys,
				"max_memory":  maxMemory,
			},
			Time: time.Now(),
		}
	}

	return HealthStatus{
		Status:  "healthy",
		Message: "Memory usage is normal",
		Details: map[string]interface{}{
			"alloc":       memStats.Alloc,
			"total_alloc": memStats.TotalAlloc,
			"sys":         memStats.Sys,
		},
		Time: time.Now(),
	}
}

// GoroutineHealthCheck checks goroutine count
type GoroutineHealthCheck struct {
	name          string
	logger        *zap.Logger
	maxGoroutines int
}

func (g *GoroutineHealthCheck) Name() string {
	return g.name
}

func (g *GoroutineHealthCheck) Check(ctx context.Context) HealthStatus {
	count := runtime.NumGoroutine()

	if count > g.maxGoroutines {
		return HealthStatus{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Too many goroutines: %d", count),
			Details: map[string]interface{}{
				"count":          count,
				"max_goroutines": g.maxGoroutines,
			},
			Time: time.Now(),
		}
	}

	return HealthStatus{
		Status:  "healthy",
		Message: "Goroutine count is normal",
		Details: map[string]interface{}{
			"count": count,
		},
		Time: time.Now(),
	}
}

// CPUHealthCheck checks CPU usage
type CPUHealthCheck struct {
	name        string
	logger      *zap.Logger
	maxCPUUsage float64
}

func (c *CPUHealthCheck) Name() string {
	return c.name
}

func (c *CPUHealthCheck) Check(ctx context.Context) HealthStatus {
	// Simplified CPU usage check
	cpuUsage := getCPUUsage()

	if cpuUsage > c.maxCPUUsage {
		return HealthStatus{
			Status:  "unhealthy",
			Message: fmt.Sprintf("CPU usage too high: %.2f%%", cpuUsage),
			Details: map[string]interface{}{
				"cpu_usage":     cpuUsage,
				"max_cpu_usage": c.maxCPUUsage,
			},
			Time: time.Now(),
		}
	}

	return HealthStatus{
		Status:  "healthy",
		Message: "CPU usage is normal",
		Details: map[string]interface{}{
			"cpu_usage": cpuUsage,
		},
		Time: time.Now(),
	}
}

// DatabaseHealthCheck checks database connectivity
type DatabaseHealthCheck struct {
	name     string
	logger   *zap.Logger
	dbClient interface{} // This would be a database client
}

func (d *DatabaseHealthCheck) Name() string {
	return d.name
}

func (d *DatabaseHealthCheck) Check(ctx context.Context) HealthStatus {
	// Simplified database health check
	// In a real implementation, this would ping the database

	return HealthStatus{
		Status:  "healthy",
		Message: "Database connection is healthy",
		Details: map[string]interface{}{
			"connection_pool": "active",
		},
		Time: time.Now(),
	}
}

// ExternalServiceHealthCheck checks external service connectivity
type ExternalServiceHealthCheck struct {
	name    string
	logger  *zap.Logger
	url     string
	timeout time.Duration
}

func (e *ExternalServiceHealthCheck) Name() string {
	return e.name
}

func (e *ExternalServiceHealthCheck) Check(ctx context.Context) HealthStatus {
	client := &http.Client{
		Timeout: e.timeout,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", e.url, nil)
	if err != nil {
		return HealthStatus{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Failed to create request: %v", err),
			Time:    time.Now(),
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return HealthStatus{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Service unavailable: %v", err),
			Time:    time.Now(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return HealthStatus{
			Status:  "healthy",
			Message: "Service is responding",
			Details: map[string]interface{}{
				"status_code": resp.StatusCode,
				"url":         e.url,
			},
			Time: time.Now(),
		}
	}

	return HealthStatus{
		Status:  "unhealthy",
		Message: fmt.Sprintf("Service returned status: %d", resp.StatusCode),
		Details: map[string]interface{}{
			"status_code": resp.StatusCode,
			"url":         e.url,
		},
		Time: time.Now(),
	}
}
