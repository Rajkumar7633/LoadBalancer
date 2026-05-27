package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Monitoring provides comprehensive monitoring and metrics
type Monitoring struct {
	logger        *zap.Logger
	config        MonitoringConfig
	metrics       *Metrics
	healthChecker *HealthChecker
	alertManager  *AlertManager
	perfMonitor   *PerformanceMonitor

	// Runtime stats
	startTime    time.Time
	requestCount int64
	errorCount   int64

	mu sync.RWMutex
}

// MonitoringConfig contains monitoring configuration
type MonitoringConfig struct {
	EnableMetrics       bool          `json:"enable_metrics"`
	EnableHealthCheck   bool          `json:"enable_health_check"`
	EnableAlerts        bool          `json:"enable_alerts"`
	EnablePerfMonitor   bool          `json:"enable_perf_monitor"`
	MetricsInterval     time.Duration `json:"metrics_interval"`
	HealthCheckInterval time.Duration `json:"health_check_interval"`
	LogLevel            string        `json:"log_level"`
	LogFormat           string        `json:"log_format"`
	Logger              *zap.Logger   `json:"-"`
}

// Metrics contains all application metrics
type Metrics struct {
	// HTTP metrics
	HTTPRequests     int64 `json:"http_requests"`
	HTTPResponses    int64 `json:"http_responses"`
	HTTPErrors       int64 `json:"http_errors"`
	HTTPResponseTime int64 `json:"http_response_time_ns"`

	// Connection metrics
	ActiveConnections int64 `json:"active_connections"`
	TotalConnections  int64 `json:"total_connections"`
	ConnectionErrors  int64 `json:"connection_errors"`

	// Backend metrics
	HealthyBackends     int64 `json:"healthy_backends"`
	UnhealthyBackends   int64 `json:"unhealthy_backends"`
	BackendResponseTime int64 `json:"backend_response_time_ns"`

	// System metrics
	MemoryUsage    int64 `json:"memory_usage_bytes"`
	CPUUsage       int64 `json:"cpu_usage_percent"`
	GoroutineCount int64 `json:"goroutine_count"`
	GCCount        int64 `json:"gc_count"`

	// Advanced metrics
	CircuitBreakerTrips int64 `json:"circuit_breaker_trips"`
	RateLimitHits       int64 `json:"rate_limit_hits"`
	CacheHits           int64 `json:"cache_hits"`
	CacheMisses         int64 `json:"cache_misses"`

	mu sync.RWMutex
}

// HealthChecker provides health checking capabilities
type HealthChecker struct {
	checks map[string]HealthCheck
	mu     sync.RWMutex
	logger *zap.Logger
}

// HealthCheck represents a health check
type HealthCheck interface {
	Name() string
	Check(ctx context.Context) HealthStatus
}

// HealthStatus represents health status
type HealthStatus struct {
	Status  string                 `json:"status"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	Time    time.Time              `json:"time"`
}

// AlertManager manages alerts and notifications
type AlertManager struct {
	alerts []Alert
	rules  []AlertRule
	mu     sync.RWMutex
	logger *zap.Logger
}

// Alert represents an alert
type Alert struct {
	ID        string                 `json:"id"`
	Level     AlertLevel             `json:"level"`
	Message   string                 `json:"message"`
	Timestamp time.Time              `json:"timestamp"`
	Details   map[string]interface{} `json:"details"`
}

// AlertLevel represents alert level
type AlertLevel int

const (
	AlertInfo AlertLevel = iota
	AlertWarning
	AlertError
	AlertCritical
)

// AlertRule represents an alert rule
type AlertRule struct {
	Name      string
	Condition func(*Metrics) bool
	Level     AlertLevel
	Message   string
	Enabled   bool
}

// PerformanceMonitor monitors system performance
type PerformanceMonitor struct {
	metrics    *PerformanceMetrics
	collectors []MetricCollector
	mu         sync.RWMutex
	logger     *zap.Logger
}

// PerformanceMetrics contains performance metrics
type PerformanceMetrics struct {
	CPUUsage    float64   `json:"cpu_usage"`
	MemoryUsage float64   `json:"memory_usage"`
	DiskUsage   float64   `json:"disk_usage"`
	NetworkIO   NetworkIO `json:"network_io"`
	Timestamp   time.Time `json:"timestamp"`
}

// NetworkIO contains network I/O metrics
type NetworkIO struct {
	BytesSent   int64 `json:"bytes_sent"`
	BytesRecv   int64 `json:"bytes_recv"`
	PacketsSent int64 `json:"packets_sent"`
	PacketsRecv int64 `json:"packets_recv"`
}

// MetricCollector collects metrics
type MetricCollector interface {
	Collect() (map[string]interface{}, error)
	Name() string
}

// NewMonitoring creates a new monitoring system
func NewMonitoring(config MonitoringConfig) (*Monitoring, error) {
	// Initialize logger
	logger, err := initLogger(config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	monitoring := &Monitoring{
		logger:    logger,
		config:    config,
		metrics:   &Metrics{},
		startTime: time.Now(),
	}

	// Initialize components
	if config.EnableHealthCheck {
		monitoring.healthChecker = NewHealthChecker(logger)
	}

	if config.EnableAlerts {
		monitoring.alertManager = NewAlertManager(logger)
	}

	if config.EnablePerfMonitor {
		monitoring.perfMonitor = NewPerformanceMonitor(logger)
	}

	// Register default alert rules
	monitoring.registerDefaultAlertRules()

	return monitoring, nil
}

// initLogger initializes the logger
func initLogger(config MonitoringConfig) (*zap.Logger, error) {
	if config.Logger != nil {
		return config.Logger, nil
	}

	var zapConfig zap.Config
	if config.LogFormat == "json" {
		zapConfig = zap.NewProductionConfig()
	} else {
		zapConfig = zap.NewDevelopmentConfig()
	}

	// Set log level
	switch config.LogLevel {
	case "debug":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	case "info":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	case "warn":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	case "error":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	default:
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}

	return zapConfig.Build()
}

// Start starts the monitoring system
func (m *Monitoring) Start(ctx context.Context) {
	m.logger.Info("Starting monitoring system")

	// Start metrics collection
	if m.config.EnableMetrics {
		go m.collectMetrics(ctx)
	}

	// Start health checks
	if m.config.EnableHealthCheck {
		go m.runHealthChecks(ctx)
	}

	// Start alert monitoring
	if m.config.EnableAlerts {
		go m.monitorAlerts(ctx)
	}

	// Start performance monitoring
	if m.config.EnablePerfMonitor {
		go m.monitorPerformance(ctx)
	}
}

// collectMetrics collects metrics periodically
func (m *Monitoring) collectMetrics(ctx context.Context) {
	ticker := time.NewTicker(m.config.MetricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.updateSystemMetrics()
		}
	}
}

// updateSystemMetrics updates system metrics
func (m *Monitoring) updateSystemMetrics() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	atomic.StoreInt64(&m.metrics.MemoryUsage, int64(memStats.Alloc))
	atomic.StoreInt64(&m.metrics.GoroutineCount, int64(runtime.NumGoroutine()))
	atomic.StoreInt64(&m.metrics.GCCount, int64(memStats.NumGC))

	// Update CPU usage (simplified)
	atomic.StoreInt64(&m.metrics.CPUUsage, int64(getCPUUsage()))
}

// runHealthChecks runs health checks periodically
func (m *Monitoring) runHealthChecks(ctx context.Context) {
	ticker := time.NewTicker(m.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.performHealthChecks(ctx)
		}
	}
}

// performHealthChecks performs all health checks
func (m *Monitoring) performHealthChecks(ctx context.Context) {
	if m.healthChecker == nil {
		return
	}

	statuses := m.healthChecker.CheckAll(ctx)
	for name, status := range statuses {
		if status.Status != "healthy" {
			m.logger.Warn("Health check failed",
				zap.String("check", name),
				zap.String("status", status.Status),
				zap.String("message", status.Message))
		}
	}
}

// monitorAlerts monitors metrics and triggers alerts
func (m *Monitoring) monitorAlerts(ctx context.Context) {
	ticker := time.NewTicker(m.config.MetricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAlerts()
		}
	}
}

// checkAlerts checks all alert rules
func (m *Monitoring) checkAlerts() {
	if m.alertManager == nil {
		return
	}

	m.alertManager.CheckRules(m.GetMetrics())
}

// monitorPerformance monitors system performance
func (m *Monitoring) monitorPerformance(ctx context.Context) {
	ticker := time.NewTicker(m.config.MetricsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.perfMonitor != nil {
				m.perfMonitor.Collect()
			}
		}
	}
}

// registerDefaultAlertRules registers default alert rules
func (m *Monitoring) registerDefaultAlertRules() {
	if m.alertManager == nil {
		return
	}

	// High error rate alert
	m.alertManager.AddRule(AlertRule{
		Name: "high_error_rate",
		Condition: func(metrics *Metrics) bool {
			if metrics.HTTPRequests == 0 {
				return false
			}
			return float64(metrics.HTTPErrors)/float64(metrics.HTTPRequests) > 0.1
		},
		Level:   AlertWarning,
		Message: "High error rate detected",
		Enabled: true,
	})

	// High memory usage alert
	m.alertManager.AddRule(AlertRule{
		Name: "high_memory_usage",
		Condition: func(metrics *Metrics) bool {
			return metrics.MemoryUsage > 1024*1024*1024 // 1GB
		},
		Level:   AlertWarning,
		Message: "High memory usage detected",
		Enabled: true,
	})

	// No healthy backends alert
	m.alertManager.AddRule(AlertRule{
		Name: "no_healthy_backends",
		Condition: func(metrics *Metrics) bool {
			return metrics.HealthyBackends == 0
		},
		Level:   AlertCritical,
		Message: "No healthy backends available",
		Enabled: true,
	})
}

// RecordRequest records an HTTP request
func (m *Monitoring) RecordRequest() {
	atomic.AddInt64(&m.requestCount, 1)
	atomic.AddInt64(&m.metrics.HTTPRequests, 1)
}

// RecordResponse records an HTTP response
func (m *Monitoring) RecordResponse(duration time.Duration, isError bool) {
	atomic.AddInt64(&m.metrics.HTTPResponses, 1)
	atomic.AddInt64(&m.metrics.HTTPResponseTime, duration.Nanoseconds())

	if isError {
		atomic.AddInt64(&m.errorCount, 1)
		atomic.AddInt64(&m.metrics.HTTPErrors, 1)
	}
}

// RecordConnection records a connection
func (m *Monitoring) RecordConnection(active bool) {
	if active {
		atomic.AddInt64(&m.metrics.ActiveConnections, 1)
	} else {
		atomic.AddInt64(&m.metrics.ActiveConnections, -1)
	}
	atomic.AddInt64(&m.metrics.TotalConnections, 1)
}

// RecordBackendHealth records backend health
func (m *Monitoring) RecordBackendHealth(healthy bool) {
	if healthy {
		atomic.AddInt64(&m.metrics.HealthyBackends, 1)
	} else {
		atomic.AddInt64(&m.metrics.UnhealthyBackends, 1)
	}
}

// GetMetrics returns current metrics
func (m *Monitoring) GetMetrics() *Metrics {
	return &Metrics{
		HTTPRequests:        atomic.LoadInt64(&m.metrics.HTTPRequests),
		HTTPResponses:       atomic.LoadInt64(&m.metrics.HTTPResponses),
		HTTPErrors:          atomic.LoadInt64(&m.metrics.HTTPErrors),
		HTTPResponseTime:    atomic.LoadInt64(&m.metrics.HTTPResponseTime),
		ActiveConnections:   atomic.LoadInt64(&m.metrics.ActiveConnections),
		TotalConnections:    atomic.LoadInt64(&m.metrics.TotalConnections),
		ConnectionErrors:    atomic.LoadInt64(&m.metrics.ConnectionErrors),
		HealthyBackends:     atomic.LoadInt64(&m.metrics.HealthyBackends),
		UnhealthyBackends:   atomic.LoadInt64(&m.metrics.UnhealthyBackends),
		BackendResponseTime: atomic.LoadInt64(&m.metrics.BackendResponseTime),
		MemoryUsage:         atomic.LoadInt64(&m.metrics.MemoryUsage),
		CPUUsage:            atomic.LoadInt64(&m.metrics.CPUUsage),
		GoroutineCount:      atomic.LoadInt64(&m.metrics.GoroutineCount),
		GCCount:             atomic.LoadInt64(&m.metrics.GCCount),
		CircuitBreakerTrips: atomic.LoadInt64(&m.metrics.CircuitBreakerTrips),
		RateLimitHits:       atomic.LoadInt64(&m.metrics.RateLimitHits),
		CacheHits:           atomic.LoadInt64(&m.metrics.CacheHits),
		CacheMisses:         atomic.LoadInt64(&m.metrics.CacheMisses),
	}
}

// GetStats returns monitoring statistics
func (m *Monitoring) GetStats() MonitoringStats {
	return MonitoringStats{
		StartTime:    m.startTime,
		RequestCount: atomic.LoadInt64(&m.requestCount),
		ErrorCount:   atomic.LoadInt64(&m.errorCount),
		Uptime:       time.Since(m.startTime),
		Metrics:      m.GetMetrics(),
	}
}

// Health check implementation
func NewHealthChecker(logger *zap.Logger) *HealthChecker {
	return &HealthChecker{
		checks: make(map[string]HealthCheck),
		logger: logger,
	}
}

func (hc *HealthChecker) AddCheck(check HealthCheck) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.checks[check.Name()] = check
}

func (hc *HealthChecker) CheckAll(ctx context.Context) map[string]HealthStatus {
	hc.mu.RLock()
	checks := make(map[string]HealthCheck)
	for name, check := range hc.checks {
		checks[name] = check
	}
	hc.mu.RUnlock()

	statuses := make(map[string]HealthStatus)
	for name, check := range checks {
		statuses[name] = check.Check(ctx)
	}

	return statuses
}

// Alert manager implementation
func NewAlertManager(logger *zap.Logger) *AlertManager {
	return &AlertManager{
		alerts: make([]Alert, 0),
		rules:  make([]AlertRule, 0),
		logger: logger,
	}
}

func (am *AlertManager) AddRule(rule AlertRule) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.rules = append(am.rules, rule)
}

func (am *AlertManager) CheckRules(metrics *Metrics) {
	am.mu.RLock()
	rules := make([]AlertRule, len(am.rules))
	copy(rules, am.rules)
	am.mu.RUnlock()

	for _, rule := range rules {
		if rule.Enabled && rule.Condition(metrics) {
			alert := Alert{
				ID:        fmt.Sprintf("%s-%d", rule.Name, time.Now().Unix()),
				Level:     rule.Level,
				Message:   rule.Message,
				Timestamp: time.Now(),
				Details:   map[string]interface{}{"metrics": metrics},
			}
			am.TriggerAlert(alert)
		}
	}
}

func (am *AlertManager) TriggerAlert(alert Alert) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.alerts = append(am.alerts, alert)

	// Log alert
	fields := []zap.Field{
		zap.String("alert_id", alert.ID),
		zap.String("level", alert.Level.String()),
		zap.String("message", alert.Message),
		zap.Time("timestamp", alert.Timestamp),
	}

	switch alert.Level {
	case AlertCritical:
		am.logger.Error("Critical alert", fields...)
	case AlertError:
		am.logger.Error("Error alert", fields...)
	case AlertWarning:
		am.logger.Warn("Warning alert", fields...)
	case AlertInfo:
		am.logger.Info("Info alert", fields...)
	}
}

// Performance monitor implementation
func NewPerformanceMonitor(logger *zap.Logger) *PerformanceMonitor {
	return &PerformanceMonitor{
		metrics:    &PerformanceMetrics{},
		collectors: make([]MetricCollector, 0),
		logger:     logger,
	}
}

func (pm *PerformanceMonitor) AddCollector(collector MetricCollector) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.collectors = append(pm.collectors, collector)
}

func (pm *PerformanceMonitor) Collect() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Update basic metrics
	pm.metrics.CPUUsage = getCPUUsage()
	pm.metrics.MemoryUsage = getMemoryUsage()
	pm.metrics.Timestamp = time.Now()

	// Collect from collectors
	for _, collector := range pm.collectors {
		data, err := collector.Collect()
		if err != nil {
			pm.logger.Error("Failed to collect metrics",
				zap.String("collector", collector.Name()),
				zap.Error(err))
			continue
		}

		// Process collected data
		pm.processCollectedData(data)
	}
}

func (pm *PerformanceMonitor) processCollectedData(data map[string]interface{}) {
	// Process collected metrics data
	for key, value := range data {
		switch key {
		case "disk_usage":
			if usage, ok := value.(float64); ok {
				pm.metrics.DiskUsage = usage
			}
		case "network_io":
			if io, ok := value.(NetworkIO); ok {
				pm.metrics.NetworkIO = io
			}
		}
	}
}

// Helper functions
func getCPUUsage() float64 {
	// Simplified CPU usage calculation
	return float64(runtime.NumCPU()) * 0.1 // Placeholder
}

func getMemoryUsage() float64 {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	return float64(memStats.Alloc) / (1024 * 1024) // MB
}

// String methods
func (a AlertLevel) String() string {
	switch a {
	case AlertInfo:
		return "info"
	case AlertWarning:
		return "warning"
	case AlertError:
		return "error"
	case AlertCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// MonitoringStats contains monitoring statistics
type MonitoringStats struct {
	StartTime    time.Time     `json:"start_time"`
	RequestCount int64         `json:"request_count"`
	ErrorCount   int64         `json:"error_count"`
	Uptime       time.Duration `json:"uptime"`
	Metrics      *Metrics      `json:"metrics"`
}

// HTTP handlers for monitoring endpoints
func (m *Monitoring) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := m.GetMetrics()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func (m *Monitoring) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if m.healthChecker == nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}

	statuses := m.healthChecker.CheckAll(ctx)

	// Check if all checks are healthy
	allHealthy := true
	for _, status := range statuses {
		if status.Status != "healthy" {
			allHealthy = false
			break
		}
	}

	if allHealthy {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(statuses)
}

func (m *Monitoring) HandleAlerts(w http.ResponseWriter, r *http.Request) {
	if m.alertManager == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	m.alertManager.mu.RLock()
	alerts := make([]Alert, len(m.alertManager.alerts))
	copy(alerts, m.alertManager.alerts)
	m.alertManager.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(alerts)
}
