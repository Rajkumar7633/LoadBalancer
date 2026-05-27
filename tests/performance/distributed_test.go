package performance

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

// DistributedLoadTester handles large-scale distributed load testing
type DistributedLoadTester struct {
	config     TestConfig
	logger     *zap.Logger
	clients    []*TestClient
	stats      *TestStatistics
	httpClient *http.Client
}

// TestConfig represents load test configuration
type TestConfig struct {
	TargetURL         string         `json:"target_url"`
	ConcurrentClients int            `json:"concurrent_clients"`
	TestDuration      time.Duration  `json:"test_duration"`
	RampUpDuration    time.Duration  `json:"ramp_up_duration"`
	RequestsPerSecond int            `json:"requests_per_second"`
	PayloadSize       int            `json:"payload_size"`
	EnableHTTP2       bool           `json:"enable_http2"`
	EnableTLS         bool           `json:"enable_tls"`
	Timeout           time.Duration  `json:"timeout"`
	Endpoints         []TestEndpoint `json:"endpoints"`
}

// TestEndpoint represents a test endpoint
type TestEndpoint struct {
	Path           string            `json:"path"`
	Method         string            `json:"method"`
	Weight         int               `json:"weight"`
	Headers        map[string]string `json:"headers"`
	Payload        string            `json:"payload"`
	ExpectedStatus int               `json:"expected_status"`
}

// TestClient represents a single test client
type TestClient struct {
	ID         int
	Config     TestConfig
	HTTPClient *http.Client
	Logger     *zap.Logger
	Stats      *ClientStatistics
}

// TestStatistics aggregates statistics from all clients
type TestStatistics struct {
	TotalRequests      int64         `json:"total_requests"`
	SuccessfulRequests int64         `json:"successful_requests"`
	FailedRequests     int64         `json:"failed_requests"`
	TotalBytes         int64         `json:"total_bytes"`
	MinLatency         time.Duration `json:"min_latency"`
	MaxLatency         time.Duration `json:"max_latency"`
	AvgLatency         time.Duration `json:"avg_latency"`
	P95Latency         time.Duration `json:"p95_latency"`
	P99Latency         time.Duration `json:"p99_latency"`
	RequestsPerSecond  float64       `json:"requests_per_second"`
	ErrorRate          float64       `json:"error_rate"`
	mutex              sync.RWMutex
	latencies          []time.Duration
}

// ClientStatistics tracks statistics for a single client
type ClientStatistics struct {
	Requests     int64
	Successes    int64
	Errors       int64
	Bytes        int64
	MinLatency   time.Duration
	MaxLatency   time.Duration
	TotalLatency time.Duration
}

// NewDistributedLoadTester creates a new distributed load tester
func NewDistributedLoadTester(config TestConfig, logger *zap.Logger) *DistributedLoadTester {
	return &DistributedLoadTester{
		config: config,
		logger: logger,
		stats:  &TestStatistics{},
	}
}

// Run executes the distributed load test
func (dlt *DistributedLoadTester) Run(ctx context.Context) error {
	dlt.logger.Info("Starting distributed load test",
		zap.Int("clients", dlt.config.ConcurrentClients),
		zap.Duration("duration", dlt.config.TestDuration),
		zap.String("target", dlt.config.TargetURL))

	// Initialize HTTP client
	if err := dlt.initHTTPClient(); err != nil {
		return fmt.Errorf("failed to initialize HTTP client: %w", err)
	}

	// Create test clients
	if err := dlt.createClients(); err != nil {
		return fmt.Errorf("failed to create clients: %w", err)
	}

	// Start statistics collection
	statsCtx, cancelStats := context.WithCancel(ctx)
	defer cancelStats()
	go dlt.collectStatistics(statsCtx)

	// Execute load test
	startTime := time.Now()
	if err := dlt.executeLoadTest(ctx); err != nil {
		return fmt.Errorf("load test failed: %w", err)
	}

	// Calculate final statistics
	dlt.calculateFinalStatistics()

	dlt.logger.Info("Load test completed",
		zap.Duration("total_time", time.Since(startTime)),
		zap.Int64("total_requests", dlt.stats.TotalRequests),
		zap.Float64("rps", dlt.stats.RequestsPerSecond),
		zap.Float64("error_rate", dlt.stats.ErrorRate))

	return nil
}

// initHTTPClient initializes the HTTP client with optimal settings
func (dlt *DistributedLoadTester) initHTTPClient() error {
	transport := &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}

	if dlt.config.EnableHTTP2 {
		if err := http2.ConfigureTransport(transport); err != nil {
			return fmt.Errorf("failed to configure HTTP/2: %w", err)
		}
	}

	dlt.httpClient = &http.Client{
		Transport: transport,
		Timeout:   dlt.config.Timeout,
	}

	return nil
}

// createClients creates test clients
func (dlt *DistributedLoadTester) createClients() error {
	dlt.clients = make([]*TestClient, dlt.config.ConcurrentClients)

	for i := 0; i < dlt.config.ConcurrentClients; i++ {
		client := &TestClient{
			ID:         i,
			Config:     dlt.config,
			HTTPClient: dlt.httpClient,
			Logger:     dlt.logger.With(zap.Int("client_id", i)),
			Stats:      &ClientStatistics{},
		}
		dlt.clients[i] = client
	}

	return nil
}

// executeLoadTest runs the actual load test
func (dlt *DistributedLoadTester) executeLoadTest(ctx context.Context) error {
	var wg sync.WaitGroup
	clientChan := make(chan *TestClient, dlt.config.ConcurrentClients)

	// Start client workers
	for i := 0; i < dlt.config.ConcurrentClients; i++ {
		wg.Add(1)
		go dlt.clientWorker(ctx, &wg, clientChan)
	}

	// Ramp up clients gradually
	rampUpInterval := dlt.config.RampUpDuration / time.Duration(dlt.config.ConcurrentClients)

	for _, client := range dlt.clients {
		select {
		case clientChan <- client:
			time.Sleep(rampUpInterval)
		case <-ctx.Done():
			close(clientChan)
			wg.Wait()
			return ctx.Err()
		}
	}

	close(clientChan)
	wg.Wait()

	return nil
}

// clientWorker runs a single test client
func (dlt *DistributedLoadTester) clientWorker(ctx context.Context, wg *sync.WaitGroup, clientChan <-chan *TestClient) {
	defer wg.Done()

	client, ok := <-clientChan
	if !ok {
		return
	}

	client.Logger.Info("Client started")

	ticker := time.NewTicker(time.Second / time.Duration(dlt.config.RequestsPerSecond/dlt.config.ConcurrentClients))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			client.Logger.Info("Client stopped")
			return
		case <-ticker.C:
			dlt.executeRequest(client)
		}
	}
}

// executeRequest executes a single request
func (dlt *DistributedLoadTester) executeRequest(client *TestClient) {
	endpoint := dlt.selectWeightedEndpoint()
	startTime := time.Now()

	var resp *http.Response
	var err error

	reqURL := dlt.config.TargetURL + endpoint.Path

	if endpoint.Method == "GET" {
		resp, err = client.HTTPClient.Get(reqURL)
	} else if endpoint.Method == "POST" {
		resp, err = client.HTTPClient.Post(reqURL, "application/json",
			io.NopCloser(dlt.generatePayload()))
	}

	latency := time.Since(startTime)

	// Update client statistics
	atomic.AddInt64(&client.Stats.Requests, 1)
	atomic.AddInt64(&dlt.stats.TotalRequests, 1)

	if err != nil {
		atomic.AddInt64(&client.Stats.Errors, 1)
		atomic.AddInt64(&dlt.stats.FailedRequests, 1)
		client.Logger.Error("Request failed", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		atomic.AddInt64(&client.Stats.Errors, 1)
		atomic.AddInt64(&dlt.stats.FailedRequests, 1)
		client.Logger.Error("Failed to read response", zap.Error(err))
		return
	}

	atomic.AddInt64(&client.Stats.Bytes, int64(len(body)))
	atomic.AddInt64(&dlt.stats.TotalBytes, int64(len(body)))

	// Check response status
	if resp.StatusCode == endpoint.ExpectedStatus {
		atomic.AddInt64(&client.Stats.Successes, 1)
		atomic.AddInt64(&dlt.stats.SuccessfulRequests, 1)
	} else {
		atomic.AddInt64(&client.Stats.Errors, 1)
		atomic.AddInt64(&dlt.stats.FailedRequests, 1)
		client.Logger.Warn("Unexpected status",
			zap.Int("expected", endpoint.ExpectedStatus),
			zap.Int("actual", resp.StatusCode))
	}

	// Update latency statistics
	dlt.updateLatencyStats(latency)
}

// selectWeightedEndpoint selects an endpoint based on weight
func (dlt *DistributedLoadTester) selectWeightedEndpoint() TestEndpoint {
	totalWeight := 0
	for _, endpoint := range dlt.config.Endpoints {
		totalWeight += endpoint.Weight
	}

	random := rand.Intn(totalWeight)
	currentWeight := 0

	for _, endpoint := range dlt.config.Endpoints {
		currentWeight += endpoint.Weight
		if random < currentWeight {
			return endpoint
		}
	}

	return dlt.config.Endpoints[0]
}

// generatePayload generates test payload
func (dlt *DistributedLoadTester) generatePayload() io.Reader {
	if dlt.config.PayloadSize <= 0 {
		return nil
	}

	payload := make([]byte, dlt.config.PayloadSize)
	rand.Read(payload)
	return io.NopCloser(dlt.generateDataStream(payload))
}

// generateDataStream creates a data stream from payload
func (dlt *DistributedLoadTester) generateDataStream(data []byte) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		writer.Write(data)
	}()
	return reader
}

// updateLatencyStats updates latency statistics
func (dlt *DistributedLoadTester) updateLatencyStats(latency time.Duration) {
	dlt.stats.mutex.Lock()
	defer dlt.stats.mutex.Unlock()

	dlt.stats.latencies = append(dlt.stats.latencies, latency)

	// Keep only last 10000 latencies to prevent memory issues
	if len(dlt.stats.latencies) > 10000 {
		dlt.stats.latencies = dlt.stats.latencies[1:]
	}
}

// collectStatistics periodically collects and reports statistics
func (dlt *DistributedLoadTester) collectStatistics(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dlt.calculateCurrentStats()
			dlt.logger.Info("Current stats",
				zap.Int64("total_requests", dlt.stats.TotalRequests),
				zap.Float64("rps", dlt.stats.RequestsPerSecond),
				zap.Float64("error_rate", dlt.stats.ErrorRate),
				zap.Duration("avg_latency", dlt.stats.AvgLatency))
		}
	}
}

// calculateCurrentStats calculates current statistics
func (dlt *DistributedLoadTester) calculateCurrentStats() {
	dlt.stats.mutex.Lock()
	defer dlt.stats.mutex.Unlock()

	if len(dlt.stats.latencies) == 0 {
		return
	}

	// Calculate percentiles
	sortedLatencies := make([]time.Duration, len(dlt.stats.latencies))
	copy(sortedLatencies, dlt.stats.latencies)

	// Simple sort (in production, use more efficient algorithm)
	for i := 0; i < len(sortedLatencies); i++ {
		for j := i + 1; j < len(sortedLatencies); j++ {
			if sortedLatencies[i] > sortedLatencies[j] {
				sortedLatencies[i], sortedLatencies[j] = sortedLatencies[j], sortedLatencies[i]
			}
		}
	}

	dlt.stats.MinLatency = sortedLatencies[0]
	dlt.stats.MaxLatency = sortedLatencies[len(sortedLatencies)-1]

	p95Index := int(float64(len(sortedLatencies)) * 0.95)
	p99Index := int(float64(len(sortedLatencies)) * 0.99)

	if p95Index < len(sortedLatencies) {
		dlt.stats.P95Latency = sortedLatencies[p95Index]
	}
	if p99Index < len(sortedLatencies) {
		dlt.stats.P99Latency = sortedLatencies[p99Index]
	}

	// Calculate average
	var totalLatency time.Duration
	for _, latency := range sortedLatencies {
		totalLatency += latency
	}
	dlt.stats.AvgLatency = totalLatency / time.Duration(len(sortedLatencies))

	// Calculate RPS and error rate
	if dlt.stats.TotalRequests > 0 {
		dlt.stats.ErrorRate = float64(dlt.stats.FailedRequests) / float64(dlt.stats.TotalRequests)
	}
}

// calculateFinalStatistics calculates final test statistics
func (dlt *DistributedLoadTester) calculateFinalStatistics() {
	dlt.calculateCurrentStats()

	// Calculate RPS based on test duration
	if dlt.config.TestDuration > 0 {
		dlt.stats.RequestsPerSecond = float64(dlt.stats.TotalRequests) / dlt.config.TestDuration.Seconds()
	}
}

// GetStatistics returns current test statistics
func (dlt *DistributedLoadTester) GetStatistics() *TestStatistics {
	dlt.stats.mutex.RLock()
	defer dlt.stats.mutex.RUnlock()
	return &TestStatistics{
		TotalRequests:      dlt.stats.TotalRequests,
		SuccessfulRequests: dlt.stats.SuccessfulRequests,
		FailedRequests:     dlt.stats.FailedRequests,
		TotalBytes:         dlt.stats.TotalBytes,
		MinLatency:         dlt.stats.MinLatency,
		MaxLatency:         dlt.stats.MaxLatency,
		AvgLatency:         dlt.stats.AvgLatency,
		P95Latency:         dlt.stats.P95Latency,
		P99Latency:         dlt.stats.P99Latency,
		RequestsPerSecond:  dlt.stats.RequestsPerSecond,
		ErrorRate:          dlt.stats.ErrorRate,
		latencies:          append([]time.Duration(nil), dlt.stats.latencies...),
	}
}

// CreateTestConfig100K creates a configuration for 100K RPS test
func CreateTestConfig100K(targetURL string) TestConfig {
	return TestConfig{
		TargetURL:         targetURL,
		ConcurrentClients: 1000,
		TestDuration:      5 * time.Minute,
		RampUpDuration:    30 * time.Second,
		RequestsPerSecond: 100000,
		PayloadSize:       1024,
		EnableHTTP2:       true,
		EnableTLS:         true,
		Timeout:           10 * time.Second,
		Endpoints: []TestEndpoint{
			{
				Path:           "/api/users",
				Method:         "GET",
				Weight:         30,
				ExpectedStatus: 200,
			},
			{
				Path:           "/api/products",
				Method:         "GET",
				Weight:         25,
				ExpectedStatus: 200,
			},
			{
				Path:           "/api/orders",
				Method:         "GET",
				Weight:         20,
				ExpectedStatus: 200,
			},
			{
				Path:           "/api/data",
				Method:         "POST",
				Weight:         15,
				ExpectedStatus: 201,
			},
			{
				Path:           "/api/health",
				Method:         "GET",
				Weight:         10,
				ExpectedStatus: 200,
			},
		},
	}
}
