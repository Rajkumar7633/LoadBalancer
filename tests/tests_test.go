package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"loadbalancer/internal/advanced_routing"
	"loadbalancer/internal/backend"
	"loadbalancer/internal/config"
	"loadbalancer/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// TestWorkerPoolIntegration tests worker pool integration
func TestWorkerPoolIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create worker pool
	config := core.WorkerPoolConfig{
		Workers:       4,
		MaxQueueSize:  100,
		WorkerTimeout: 5 * time.Second,
		EnableMetrics: true,
		Logger:        logger,
	}

	pool := core.NewWorkerPool(config)
	require.NotNil(t, pool)

	pool.Start()
	defer pool.Stop()

	// Test task submission
	task := core.Task{
		ID: "test-task",
		Function: func() error {
			time.Sleep(100 * time.Millisecond)
			return nil
		},
		Timeout: 1 * time.Second,
		Retry:   1,
	}

	err := pool.Submit(task)
	assert.NoError(t, err)

	// Wait for task completion
	time.Sleep(200 * time.Millisecond)

	stats := pool.GetStats()
	assert.Equal(t, int64(1), stats.TotalTasks)
	assert.Equal(t, int64(1), stats.CompletedTasks)
	assert.Equal(t, int64(0), stats.FailedTasks)
}

// TestCircuitBreakerIntegration tests circuit breaker integration
func TestCircuitBreakerIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create circuit breaker
	config := core.DefaultCircuitBreakerConfig("test-circuit")
	config.Logger = logger

	cb := core.NewCircuitBreaker(config)
	require.NotNil(t, cb)

	// Test successful execution
	err := cb.Execute(context.Background(), func() error {
		return nil
	})
	assert.NoError(t, err)

	// Test failed execution to trigger circuit breaker
	for i := 0; i < 10; i++ {
		err := cb.Execute(context.Background(), func() error {
			return assert.AnError
		})
		assert.Error(t, err)
	}

	// Circuit should be open now
	assert.Equal(t, core.StateOpen, cb.GetState())

	// Test that execution fails when circuit is open
	err = cb.Execute(context.Background(), func() error {
		return nil
	})
	assert.Error(t, err)
	assert.Equal(t, core.ErrCircuitBreakerOpen, err)
}

// TestMonitoringIntegration tests monitoring integration
func TestMonitoringIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create monitoring system
	config := core.MonitoringConfig{
		EnableMetrics:       true,
		EnableHealthCheck:   true,
		EnableAlerts:        true,
		EnablePerfMonitor:   true,
		MetricsInterval:     1 * time.Second,
		HealthCheckInterval: 5 * time.Second,
		LogLevel:            "info",
		LogFormat:           "json",
		Logger:              logger,
	}

	monitoring, err := core.NewMonitoring(config)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start monitoring
	monitoring.Start(ctx)

	// Simulate some activity
	for i := 0; i < 10; i++ {
		monitoring.RecordRequest()
		monitoring.RecordResponse(100*time.Millisecond, i%10 == 0) // 10% error rate
		monitoring.RecordConnection(i%2 == 0)
		monitoring.RecordBackendHealth(i%5 != 0) // 20% unhealthy
	}

	// Wait for metrics to be processed
	time.Sleep(2 * time.Second)

	// Check metrics
	metrics := monitoring.GetMetrics()
	assert.Equal(t, int64(10), metrics.HTTPRequests)
	assert.Equal(t, int64(10), metrics.HTTPResponses)
	assert.Equal(t, int64(1), metrics.HTTPErrors)

	// Check stats
	stats := monitoring.GetStats()
	assert.Equal(t, int64(10), stats.RequestCount)
	assert.Equal(t, int64(1), stats.ErrorCount)
}

// TestGracefulShutdownIntegration tests graceful shutdown integration
func TestGracefulShutdownIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create graceful shutdown manager
	config := core.GracefulShutdownConfig{
		ShutdownTimeout:    10 * time.Second,
		DrainTimeout:       5 * time.Second,
		ForceShutdownAfter: 30 * time.Second,
		EnableHealthCheck:  true,
		HealthCheckPath:    "/health",
		Logger:             logger,
	}

	gs := core.NewGracefulShutdown(config)
	require.NotNil(t, gs)

	// Create worker pool
	workerConfig := core.WorkerPoolConfig{
		Workers:       4,
		MaxQueueSize:  100,
		WorkerTimeout: 5 * time.Second,
		EnableMetrics: true,
		Logger:        logger,
	}

	pool := core.NewWorkerPool(workerConfig)
	pool.Start()
	gs.RegisterWorkerPool(pool)

	// Start graceful shutdown
	gs.Start()

	// Test shutdown status
	assert.False(t, gs.IsShuttingDown())

	// Get status
	status := gs.GetShutdownStatus()
	assert.False(t, status.IsShuttingDown)
}

// TestBasicIntegration tests basic load balancer integration
func TestBasicIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)
	_ = logger // Use logger to avoid unused variable warning

	// Create test backends
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Backend 1 Response"))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Backend 2 Response"))
	}))
	defer backend2.Close()

	// Create simple configuration
	cfg := &config.Config{
		Port:           8080,
		RequestTimeout: 30,
		Backends: []config.BackendConfig{
			{
				URL:             backend1.URL,
				Weight:          1,
				MaxConnections:  100,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
			{
				URL:             backend2.URL,
				Weight:          1,
				MaxConnections:  100,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
		},
		Routing: config.RoutingConfig{
			Algorithm: "round_robin",
		},
	}

	// Initialize advanced routing manager
	arm, err := advanced_routing.NewAdvancedRoutingManager(*cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, arm)

	// Set up backends
	backend1URL, _ := url.Parse(backend1.URL)
	backend2URL, _ := url.Parse(backend2.URL)

	backends := []backend.Backend{
		backend.NewBackend(backend1URL, 1, 100, "/health", 5*time.Second),
		backend.NewBackend(backend2URL, 1, 100, "/health", 5*time.Second),
	}
	arm.SetBackends(backends)

	// Test basic routing
	t.Run("BasicRouting", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		selectedBackend := arm.RouteRequest(context.Background(), req)
		assert.NotNil(t, selectedBackend)
	})

	// Test concurrent requests
	t.Run("ConcurrentRequests", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest("GET", "/", nil)
			selectedBackend := arm.RouteRequest(context.Background(), req)
			assert.NotNil(t, selectedBackend)
		}
	})
}
