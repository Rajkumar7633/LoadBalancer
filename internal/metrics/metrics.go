package metrics

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Collector struct {
	requestsTotal     *prometheus.CounterVec
	requestDuration   *prometheus.HistogramVec
	requestSize       *prometheus.HistogramVec
	responseSize      *prometheus.HistogramVec
	activeConnections prometheus.Gauge
	backendStatus     *prometheus.GaugeVec
	rateLimitHits     *prometheus.CounterVec
	circuitBreaker    *prometheus.CounterVec
	healthCheckStatus *prometheus.GaugeVec

	mu sync.RWMutex
}

func NewCollector() *Collector {
	c := &Collector{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadbalancer_requests_total",
				Help: "Total number of requests processed",
			},
			[]string{"method", "path", "status", "backend"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadbalancer_request_duration_seconds",
				Help:    "Request duration in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path", "backend"},
		),
		requestSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadbalancer_request_size_bytes",
				Help:    "Request size in bytes",
				Buckets: []float64{100, 1000, 10000, 100000, 1000000},
			},
			[]string{"method", "path"},
		),
		responseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadbalancer_response_size_bytes",
				Help:    "Response size in bytes",
				Buckets: []float64{100, 1000, 10000, 100000, 1000000},
			},
			[]string{"method", "path", "status"},
		),
		activeConnections: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "loadbalancer_active_connections",
				Help: "Number of active connections",
			},
		),
		backendStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "loadbalancer_backend_status",
				Help: "Backend status (1 = healthy, 0 = unhealthy)",
			},
			[]string{"backend", "url"},
		),
		rateLimitHits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadbalancer_rate_limit_hits_total",
				Help: "Total number of rate limit hits",
			},
			[]string{"client_ip", "limit_type"},
		),
		circuitBreaker: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadbalancer_circuit_breaker_trips_total",
				Help: "Total number of circuit breaker trips",
			},
			[]string{"backend", "reason"},
		),
		healthCheckStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "loadbalancer_health_check_status",
				Help: "Health check status (1 = success, 0 = failure)",
			},
			[]string{"backend", "check_type"},
		),
	}

	// Register metrics with Prometheus
	prometheus.MustRegister(c.requestsTotal)
	prometheus.MustRegister(c.requestDuration)
	prometheus.MustRegister(c.requestSize)
	prometheus.MustRegister(c.responseSize)
	prometheus.MustRegister(c.activeConnections)
	prometheus.MustRegister(c.backendStatus)
	prometheus.MustRegister(c.rateLimitHits)
	prometheus.MustRegister(c.circuitBreaker)
	prometheus.MustRegister(c.healthCheckStatus)

	return c
}

func (c *Collector) RecordRequest(method, path, status, backend string, duration time.Duration) {
	c.requestsTotal.WithLabelValues(method, path, status, backend).Inc()
	c.requestDuration.WithLabelValues(method, path, backend).Observe(duration.Seconds())
}

func (c *Collector) RecordRequestSize(method, path string, size int) {
	c.requestSize.WithLabelValues(method, path).Observe(float64(size))
}

func (c *Collector) RecordResponseSize(method, path, status string, size int) {
	c.responseSize.WithLabelValues(method, path, status).Observe(float64(size))
}

func (c *Collector) SetActiveConnections(count int) {
	c.activeConnections.Set(float64(count))
}

func (c *Collector) SetBackendStatus(backend, url string, healthy bool) {
	status := 0.0
	if healthy {
		status = 1.0
	}
	c.backendStatus.WithLabelValues(backend, url).Set(status)
}

func (c *Collector) RecordRateLimitHit(clientIP, limitType string) {
	c.rateLimitHits.WithLabelValues(clientIP, limitType).Inc()
}

func (c *Collector) RecordCircuitBreakerTrip(backend, reason string) {
	c.circuitBreaker.WithLabelValues(backend, reason).Inc()
}

func (c *Collector) RecordHealthCheckStatus(backend, checkType string, success bool) {
	status := 0.0
	if success {
		status = 1.0
	}
	c.healthCheckStatus.WithLabelValues(backend, checkType).Set(status)
}

func (c *Collector) StartServer(port int) {
	http.Handle("/metrics", promhttp.Handler())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.DefaultServeMux,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		// Log error but don't crash the application
	}
}
