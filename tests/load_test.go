package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loadbalancer/internal/advanced_routing"
	backendpkg "loadbalancer/internal/backend"
	"loadbalancer/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LoadTestConfig contains load test configuration
type LoadTestConfig struct {
	NumClients        int
	RequestsPerClient int
	Duration          time.Duration
	RampUpTime        time.Duration
	TargetURL         string
	Concurrency       int
	Timeout           time.Duration
}

// LoadTestResult contains load test results
type LoadTestResult struct {
	TotalRequests      int64         `json:"total_requests"`
	SuccessfulRequests int64         `json:"successful_requests"`
	FailedRequests     int64         `json:"failed_requests"`
	TotalDuration      time.Duration `json:"total_duration"`
	AverageLatency     time.Duration `json:"average_latency"`
	MinLatency         time.Duration `json:"min_latency"`
	MaxLatency         time.Duration `json:"max_latency"`
	P95Latency         time.Duration `json:"p95_latency"`
	P99Latency         time.Duration `json:"p99_latency"`
	RequestsPerSecond  float64       `json:"requests_per_second"`
	ErrorRate          float64       `json:"error_rate"`
	MemoryUsage        int64         `json:"memory_usage_bytes"`
	CPUUsage           float64       `json:"cpu_usage_percent"`
}

// TestLoadPerformance tests load performance under various conditions
func TestLoadPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	// Create test backends
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond) // Simulate processing time
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Backend 1"))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond) // Simulate processing time
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Backend 2"))
	}))
	defer backend2.Close()

	backend3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond) // Simulate processing time
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Backend 3"))
	}))
	defer backend3.Close()

	// Create load balancer configuration
	cfg := &config.Config{
		Port:           8080,
		RequestTimeout: 30,
		Backends: []config.BackendConfig{
			{
				URL:             backend1.URL,
				Weight:          1,
				MaxConnections:  1000,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
			{
				URL:             backend2.URL,
				Weight:          1,
				MaxConnections:  1000,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
			{
				URL:             backend3.URL,
				Weight:          1,
				MaxConnections:  1000,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
		},
		Routing: config.RoutingConfig{
			Algorithm: "round_robin",
		},
	}

	// Initialize load balancer
	arm, err := advanced_routing.NewAdvancedRoutingManager(*cfg, nil)
	require.NoError(t, err)

	backend1URL, _ := url.Parse(backend1.URL)
	backend2URL, _ := url.Parse(backend2.URL)
	backend3URL, _ := url.Parse(backend3.URL)

	backends := []backendpkg.Backend{
		backendpkg.NewBackend(backend1URL, 1, 1000, "/health", 5*time.Second),
		backendpkg.NewBackend(backend2URL, 1, 1000, "/health", 5*time.Second),
		backendpkg.NewBackend(backend3URL, 1, 1000, "/health", 5*time.Second),
	}
	arm.SetBackends(backends)

	// Test scenarios
	testScenarios := []LoadTestConfig{
		{
			NumClients:        10,
			RequestsPerClient: 100,
			Duration:          30 * time.Second,
			RampUpTime:        5 * time.Second,
			Concurrency:       10,
			Timeout:           30 * time.Second,
		},
		{
			NumClients:        50,
			RequestsPerClient: 200,
			Duration:          60 * time.Second,
			RampUpTime:        10 * time.Second,
			Concurrency:       50,
			Timeout:           30 * time.Second,
		},
	}

	for _, scenario := range testScenarios {
		t.Run(fmt.Sprintf("LoadTest_%dclients_%drequests", scenario.NumClients, scenario.RequestsPerClient), func(t *testing.T) {
			result := runLoadTest(t, arm, scenario)

			t.Logf("Load Test Results:")
			t.Logf("  Total Requests: %d", result.TotalRequests)
			t.Logf("  Successful Requests: %d", result.SuccessfulRequests)
			t.Logf("  Failed Requests: %d", result.FailedRequests)
			t.Logf("  Average Latency: %v", result.AverageLatency)
			t.Logf("  P95 Latency: %v", result.P95Latency)
			t.Logf("  P99 Latency: %v", result.P99Latency)
			t.Logf("  Requests/sec: %.2f", result.RequestsPerSecond)
			t.Logf("  Error Rate: %.2f%%", result.ErrorRate*100)

			// Assertions for performance requirements
			assert.Greater(t, result.SuccessfulRequests, int64(0))
			assert.Less(t, result.ErrorRate, 0.05) // Less than 5% error rate
			assert.Less(t, result.AverageLatency, 100*time.Millisecond)
			assert.Less(t, result.P95Latency, 200*time.Millisecond)
			assert.Greater(t, result.RequestsPerSecond, 100.0)
		})
	}
}

// runLoadTest executes a load test
func runLoadTest(t *testing.T, arm *advanced_routing.AdvancedRoutingManager, config LoadTestConfig) LoadTestResult {
	var (
		totalRequests      int64
		successfulRequests int64
		failedRequests     int64
		minLatency         time.Duration = time.Hour
		maxLatency         time.Duration
		totalLatency       int64
		latencies          []time.Duration
		mu                 sync.Mutex
		wg                 sync.WaitGroup
	)

	startTime := time.Now()

	// Ramp up clients gradually
	rampUpInterval := config.RampUpTime / time.Duration(config.NumClients)

	for i := 0; i < config.NumClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			// Stagger client start times for ramp-up
			time.Sleep(time.Duration(clientID) * rampUpInterval)

			for j := 0; j < config.RequestsPerClient; j++ {
				requestStart := time.Now()

				// Create request
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-Client-ID", fmt.Sprintf("client-%d", clientID))
				req.Header.Set("X-Request-ID", fmt.Sprintf("request-%d-%d", clientID, j))

				// Route request
				selectedBackend := arm.RouteRequest(context.Background(), req)

				latency := time.Since(requestStart)

				atomic.AddInt64(&totalRequests, 1)
				atomic.AddInt64(&totalLatency, latency.Nanoseconds())

				mu.Lock()
				latencies = append(latencies, latency)
				if latency < minLatency {
					minLatency = latency
				}
				if latency > maxLatency {
					maxLatency = latency
				}
				mu.Unlock()

				if selectedBackend != nil {
					atomic.AddInt64(&successfulRequests, 1)
				} else {
					atomic.AddInt64(&failedRequests, 1)
				}

				// Add small delay between requests
				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	// Calculate statistics
	mu.Lock()
	defer mu.Unlock()

	// Calculate percentiles - use efficient sort
	sortedLatencies := make([]time.Duration, len(latencies))
	copy(sortedLatencies, latencies)

	// Use Go's built-in sort for efficiency
	sort.Slice(sortedLatencies, func(i, j int) bool {
		return sortedLatencies[i] < sortedLatencies[j]
	})

	var p95Latency, p99Latency time.Duration
	if len(sortedLatencies) > 0 {
		p95Index := int(float64(len(sortedLatencies)) * 0.95)
		p99Index := int(float64(len(sortedLatencies)) * 0.99)

		if p95Index < len(sortedLatencies) {
			p95Latency = sortedLatencies[p95Index]
		}
		if p99Index < len(sortedLatencies) {
			p99Latency = sortedLatencies[p99Index]
		}
	}

	var avgLatency time.Duration
	if totalRequests > 0 {
		avgLatency = time.Duration(totalLatency / totalRequests)
	}

	requestsPerSecond := float64(totalRequests) / totalDuration.Seconds()

	var errorRate float64
	if totalRequests > 0 {
		errorRate = float64(failedRequests) / float64(totalRequests)
	}

	// Get memory usage
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	memoryUsage := int64(memStats.Alloc)

	return LoadTestResult{
		TotalRequests:      totalRequests,
		SuccessfulRequests: successfulRequests,
		FailedRequests:     failedRequests,
		TotalDuration:      totalDuration,
		AverageLatency:     avgLatency,
		MinLatency:         minLatency,
		MaxLatency:         maxLatency,
		P95Latency:         p95Latency,
		P99Latency:         p99Latency,
		RequestsPerSecond:  requestsPerSecond,
		ErrorRate:          errorRate,
		MemoryUsage:        memoryUsage,
		CPUUsage:           getCPUUsage(),
	}
}

// TestStressTest tests the system under extreme stress
func TestStressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Create test backends with varying performance
	backends := make([]*httptest.Server, 5)
	for i := 0; i < 5; i++ {
		idx := i
		backends[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Simulate varying response times
			delay := time.Duration(idx*5) * time.Millisecond
			time.Sleep(delay)

			// Simulate occasional failures
			if idx == 4 && time.Now().Unix()%10 == 0 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write([]byte(fmt.Sprintf("Backend %d", idx)))
		}))
		defer backends[i].Close()
	}

	// Create stress test configuration
	cfg := &config.Config{
		Port:           8080,
		RequestTimeout: 30,
		Backends:       make([]config.BackendConfig, 5),
		Routing: config.RoutingConfig{
			Algorithm: "round_robin",
		},
	}

	for i, backend := range backends {
		cfg.Backends[i] = config.BackendConfig{
			URL:             backend.URL,
			Weight:          1,
			MaxConnections:  2000,
			HealthCheckPath: "/health",
			Timeout:         10 * time.Second,
		}
	}

	// Initialize load balancer
	arm, err := advanced_routing.NewAdvancedRoutingManager(*cfg, nil)
	require.NoError(t, err)

	backendURLs := make([]*url.URL, 5)
	backendList := make([]backendpkg.Backend, 5)
	for i, backend := range backends {
		backendURLs[i], _ = url.Parse(backend.URL)
		backendList[i] = backendpkg.NewBackend(backendURLs[i], 1, 2000, "/health", 10*time.Second)
	}
	arm.SetBackends(backendList)

	// Stress test configuration
	stressConfig := LoadTestConfig{
		NumClients:        50,
		RequestsPerClient: 100,
		Duration:          60 * time.Second, // 1 minute for testing
		RampUpTime:        10 * time.Second,
		Concurrency:       50,
		Timeout:           30 * time.Second,
	}

	t.Run("StressTest", func(t *testing.T) {
		result := runLoadTest(t, arm, stressConfig)

		t.Logf("Stress Test Results:")
		t.Logf("  Total Requests: %d", result.TotalRequests)
		t.Logf("  Successful Requests: %d", result.SuccessfulRequests)
		t.Logf("  Failed Requests: %d", result.FailedRequests)
		t.Logf("  Average Latency: %v", result.AverageLatency)
		t.Logf("  P95 Latency: %v", result.P95Latency)
		t.Logf("  P99 Latency: %v", result.P99Latency)
		t.Logf("  Requests/sec: %.2f", result.RequestsPerSecond)
		t.Logf("  Error Rate: %.2f%%", result.ErrorRate*100)
		t.Logf("  Memory Usage: %d MB", result.MemoryUsage/1024/1024)
		t.Logf("  CPU Usage: %.2f%%", result.CPUUsage)

		// Stress test assertions
		assert.Greater(t, result.SuccessfulRequests, int64(result.TotalRequests*70/100)) // At least 70% success
		assert.Less(t, result.ErrorRate, 0.30)                                           // Less than 30% error rate
		assert.Greater(t, result.RequestsPerSecond, 500.0)                               // Good throughput
		assert.Less(t, result.MemoryUsage, int64(1024*1024*1024))                        // Less than 1GB memory
	})
}

// TestMemoryLeak tests for memory leaks under sustained load
func TestMemoryLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	// Create test backend
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer backend.Close()

	// Create load balancer
	cfg := &config.Config{
		Port:           8080,
		RequestTimeout: 30,
		Backends: []config.BackendConfig{
			{
				URL:             backend.URL,
				Weight:          1,
				MaxConnections:  1000,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
		},
		Routing: config.RoutingConfig{
			Algorithm: "round_robin",
		},
	}

	arm, err := advanced_routing.NewAdvancedRoutingManager(*cfg, nil)
	require.NoError(t, err)

	backendURL, _ := url.Parse(backend.URL)
	backends := []backendpkg.Backend{
		backendpkg.NewBackend(backendURL, 1, 1000, "/health", 5*time.Second),
	}
	arm.SetBackends(backends)

	// Monitor memory usage
	var memoryReadings []int64
	numReadings := 5 // Reduced for testing
	readingInterval := 5 * time.Second

	// Start memory monitoring
	stopMonitoring := make(chan bool)
	go func() {
		for {
			select {
			case <-stopMonitoring:
				return
			default:
				var memStats runtime.MemStats
				runtime.ReadMemStats(&memStats)
				memoryReadings = append(memoryReadings, int64(memStats.Alloc))
				time.Sleep(readingInterval)
			}
		}
	}()

	// Run sustained load
	loadConfig := LoadTestConfig{
		NumClients:        50,
		RequestsPerClient: 100,
		Duration:          time.Duration(numReadings) * readingInterval,
		RampUpTime:        5 * time.Second,
		Concurrency:       50,
		Timeout:           30 * time.Second,
	}

	result := runLoadTest(t, arm, loadConfig)

	// Stop memory monitoring
	stopMonitoring <- true
	time.Sleep(1 * time.Second) // Allow monitoring to stop

	t.Logf("Memory Leak Test Results:")
	t.Logf("  Total Requests: %d", result.TotalRequests)
	t.Logf("  Memory Readings: %d", len(memoryReadings))

	if len(memoryReadings) >= 2 {
		initialMemory := memoryReadings[0]
		finalMemory := memoryReadings[len(memoryReadings)-1]
		memoryGrowth := finalMemory - initialMemory
		memoryGrowthRate := float64(memoryGrowth) / float64(initialMemory) * 100

		t.Logf("  Initial Memory: %d MB", initialMemory/1024/1024)
		t.Logf("  Final Memory: %d MB", finalMemory/1024/1024)
		t.Logf("  Memory Growth: %d MB (%.2f%%)", memoryGrowth/1024/1024, memoryGrowthRate)

		// Assert memory growth is reasonable (less than 50% growth)
		assert.Less(t, memoryGrowthRate, 50.0, "Memory growth should be less than 50%")
	}
}

// BenchmarkLoadTest benchmarks the load balancer performance
func BenchmarkLoadTest(b *testing.B) {
	// Create test backend
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer backend.Close()

	// Create load balancer
	cfg := &config.Config{
		Port:           8080,
		RequestTimeout: 30,
		Backends: []config.BackendConfig{
			{
				URL:             backend.URL,
				Weight:          1,
				MaxConnections:  1000,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
		},
		Routing: config.RoutingConfig{
			Algorithm: "round_robin",
		},
	}

	arm, err := advanced_routing.NewAdvancedRoutingManager(*cfg, nil)
	require.NoError(b, err)

	backendURL, _ := url.Parse(backend.URL)
	backends := []backendpkg.Backend{
		backendpkg.NewBackend(backendURL, 1, 1000, "/health", 5*time.Second),
	}
	arm.SetBackends(backends)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/", nil)
			selectedBackend := arm.RouteRequest(context.Background(), req)
			if selectedBackend == nil {
				b.Error("Failed to route request")
			}
		}
	})
}

// Helper function to get CPU usage
func getCPUUsage() float64 {
	// Simplified CPU usage calculation
	return float64(runtime.NumCPU()) * 0.1 // Placeholder
}
