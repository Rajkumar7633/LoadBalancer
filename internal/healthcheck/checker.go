package healthcheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"loadbalancer/internal/backend"
	"loadbalancer/internal/config"
	"loadbalancer/internal/logger"
	"loadbalancer/internal/metrics"
)

type Checker struct {
	backends     []backend.Backend
	config       config.HealthCheckConfig
	logger       *logger.Logger
	metrics      *metrics.Collector
	client       *http.Client
	shutdownChan chan struct{}
	mu           sync.RWMutex
}

type HealthCheckResult struct {
	Backend   backend.Backend
	Healthy   bool
	Duration  time.Duration
	Error     error
	CheckType string
}

func NewChecker(backends []backend.Backend, cfg config.HealthCheckConfig, logger *logger.Logger, metrics *metrics.Collector) *Checker {
	// Create HTTP client for health checks
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Allow self-signed certs
			},
			DisableKeepAlives: true,
		},
	}

	return &Checker{
		backends:     backends,
		config:       cfg,
		logger:       logger,
		metrics:      metrics,
		client:       client,
		shutdownChan: make(chan struct{}),
	}
}

func (c *Checker) Start(ctx context.Context) {
	ticker := time.NewTicker(c.config.Interval)
	defer ticker.Stop()

	// Run initial health check
	c.runHealthChecks()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.shutdownChan:
			return
		case <-ticker.C:
			c.runHealthChecks()
		}
	}
}

func (c *Checker) Stop() {
	close(c.shutdownChan)
}

func (c *Checker) runHealthChecks() {
	c.mu.RLock()
	backends := make([]backend.Backend, len(c.backends))
	copy(backends, c.backends)
	c.mu.RUnlock()

	var wg sync.WaitGroup
	results := make(chan HealthCheckResult, len(backends))

	// Run health checks in parallel
	for _, backend := range backends {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := c.checkBackend(backend)
			results <- result
		}()
	}

	// Wait for all checks to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Process results
	for result := range results {
		c.processHealthCheckResult(result)
	}
}

func (c *Checker) checkBackend(backend backend.Backend) HealthCheckResult {

	// Try HTTP health check first
	if backend.GetHealthCheckPath() != "" {
		healthy, duration, err := c.performHTTPCheck(backend)
		if err == nil {
			return HealthCheckResult{
				Backend:   backend,
				Healthy:   healthy,
				Duration:  duration,
				Error:     err,
				CheckType: "http",
			}
		}
		c.logger.Warn("HTTP health check failed, trying TCP", "backend", backend.GetURL().String(), "error", err)
	}

	// Fallback to TCP health check
	healthy, duration, err := c.performTCPCheck(backend)
	return HealthCheckResult{
		Backend:   backend,
		Healthy:   healthy,
		Duration:  duration,
		Error:     err,
		CheckType: "tcp",
	}
}

func (c *Checker) performHTTPCheck(backend backend.Backend) (bool, time.Duration, error) {
	startTime := time.Now()

	// Build health check URL
	healthURL := *backend.GetURL()
	healthURL.Path = backend.GetHealthCheckPath()

	req, err := http.NewRequest("GET", healthURL.String(), nil)
	if err != nil {
		return false, time.Since(startTime), err
	}

	// Add headers
	req.Header.Set("User-Agent", "LoadBalancer-HealthCheck/1.0")
	req.Header.Set("X-Health-Check", "true")

	resp, err := c.client.Do(req)
	if err != nil {
		return false, time.Since(startTime), err
	}
	defer resp.Body.Close()

	duration := time.Since(startTime)

	// Check response status
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, duration, nil
	}

	return false, duration, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
}

func (c *Checker) performTCPCheck(backend backend.Backend) (bool, time.Duration, error) {
	startTime := time.Now()

	host := backend.GetURL().Hostname()
	port := backend.GetURL().Port()
	if port == "" {
		if backend.GetURL().Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	address := net.JoinHostPort(host, port)

	conn, err := net.DialTimeout("tcp", address, c.config.Timeout)
	if err != nil {
		return false, time.Since(startTime), err
	}
	defer conn.Close()

	duration := time.Since(startTime)
	return true, duration, nil
}

func (c *Checker) processHealthCheckResult(result HealthCheckResult) {
	backend := result.Backend

	// Update metrics
	c.metrics.RecordHealthCheckStatus(backend.GetURL().String(), result.CheckType, result.Healthy)

	if result.Healthy {
		// Success
		backend.SetHealthy(true)
		if !backend.IsHealthy() {
			c.logger.Info("Backend marked healthy",
				"backend", backend.GetURL().String(),
				"check_type", result.CheckType,
				"duration", result.Duration,
			)
		}
	} else {
		// Failure
		backend.IncrementFailureCount()
		backend.GetCircuitBreaker().RecordFailure()

		if backend.GetFailureCount() >= c.config.FailureThreshold {
			if backend.IsHealthy() {
				backend.SetHealthy(false)
				c.logger.Warn("Backend marked unhealthy",
					"backend", backend.GetURL().String(),
					"check_type", result.CheckType,
					"failure_count", backend.GetFailureCount(),
					"error", result.Error,
				)
			}
		} else {
			c.logger.Debug("Backend health check failed",
				"backend", backend.GetURL().String(),
				"check_type", result.CheckType,
				"failure_count", backend.GetFailureCount(),
				"error", result.Error,
			)
		}
	}

	// Update backend status in metrics
	c.metrics.SetBackendStatus(backend.GetURL().String(), backend.GetURL().String(), backend.IsHealthy())
}

func (c *Checker) UpdateBackends(backends []backend.Backend) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backends = backends
}

func (c *Checker) ForceHealthCheck(backend backend.Backend) {
	result := c.checkBackend(backend)
	c.processHealthCheckResult(result)
}

// Advanced health check features

func (c *Checker) PerformDeepHealthCheck(backend backend.Backend) map[string]interface{} {
	results := make(map[string]interface{})

	// HTTP Check
	if backend.GetHealthCheckPath() != "" {
		healthy, duration, err := c.performHTTPCheck(backend)
		results["http"] = map[string]interface{}{
			"healthy":  healthy,
			"duration": duration,
			"error":    err,
		}
	}

	// TCP Check
	healthy, duration, err := c.performTCPCheck(backend)
	results["tcp"] = map[string]interface{}{
		"healthy":  healthy,
		"duration": duration,
		"error":    err,
	}

	// DNS Resolution Check
	dnsHealthy, dnsDuration, dnsErr := c.performDNSCheck(backend)
	results["dns"] = map[string]interface{}{
		"healthy":  dnsHealthy,
		"duration": dnsDuration,
		"error":    dnsErr,
	}

	// SSL Certificate Check (if HTTPS)
	if backend.GetURL().Scheme == "https" {
		certHealthy, certDuration, certErr := c.performSSLCertificateCheck(backend)
		results["ssl"] = map[string]interface{}{
			"healthy":  certHealthy,
			"duration": certDuration,
			"error":    certErr,
		}
	}

	return results
}

func (c *Checker) performDNSCheck(backend backend.Backend) (bool, time.Duration, error) {
	startTime := time.Now()

	_, err := net.LookupHost(backend.GetURL().Hostname())
	if err != nil {
		return false, time.Since(startTime), err
	}

	return true, time.Since(startTime), nil
}

func (c *Checker) performSSLCertificateCheck(backend backend.Backend) (bool, time.Duration, error) {
	startTime := time.Now()

	address := fmt.Sprintf("%s:%s", backend.GetURL().Hostname(), backend.GetURL().Port())
	if backend.GetURL().Port() == "" {
		address = fmt.Sprintf("%s:443", backend.GetURL().Hostname())
	}

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: c.config.Timeout}, "tcp", address, &tls.Config{
		InsecureSkipVerify: false,
	})
	if err != nil {
		return false, time.Since(startTime), err
	}
	defer conn.Close()

	// Check certificate validity
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return false, time.Since(startTime), fmt.Errorf("no certificates presented")
	}

	cert := certs[0]
	now := time.Now()

	// Check if certificate is expired or not yet valid
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return false, time.Since(startTime), fmt.Errorf("certificate not valid at current time")
	}

	return true, time.Since(startTime), nil
}
