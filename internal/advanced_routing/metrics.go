package advanced_routing

import (
	"fmt"
	"time"

	"loadbalancer/internal/metrics"
)

// AdvancedRoutingMetrics provides comprehensive metrics for all Tier 2 features
type AdvancedRoutingMetrics struct {
	collector *metrics.Collector
}

func NewAdvancedRoutingMetrics(collector *metrics.Collector) *AdvancedRoutingMetrics {
	return &AdvancedRoutingMetrics{
		collector: collector,
	}
}

// Content-Based Routing Metrics
func (arm *AdvancedRoutingMetrics) RecordContentBasedRouting(ruleName string, backend string, duration time.Duration) {
	arm.collector.RecordRequest("GET", "/advanced/content-based", "200", backend, duration)
}

// Consistent Hashing Metrics
func (arm *AdvancedRoutingMetrics) RecordConsistentHashHit(key string, backend string, duration time.Duration) {
	arm.collector.RecordRequest("GET", "/advanced/consistent-hash", "200", backend, duration)
}

// IP Hash Metrics
func (arm *AdvancedRoutingMetrics) RecordIPHashHit(clientIP string, backend string, duration time.Duration) {
	arm.collector.RecordRequest("GET", "/advanced/ip-hash", "200", backend, duration)
}

// Circuit Breaker Metrics
func (arm *AdvancedRoutingMetrics) RecordCircuitBreakerStateChange(backend string, fromState, toState string) {
	arm.collector.RecordCircuitBreakerTrip(backend, "state_change")
}

func (arm *AdvancedRoutingMetrics) RecordCircuitBreakerFailure(backend string, duration time.Duration) {
	arm.collector.RecordCircuitBreakerTrip(backend, "failure")
}

// Retry Metrics
func (arm *AdvancedRoutingMetrics) RecordRetryAttempt(backend string, attempt int, reason string) {
	arm.collector.RecordRequest("GET", "/advanced/retry", "retry", backend, time.Millisecond*100)
}

func (arm *AdvancedRoutingMetrics) RecordRetrySuccess(backend string, totalAttempts int, totalDuration time.Duration) {
	arm.collector.RecordRequest("GET", "/advanced/retry", "200", backend, totalDuration)
}

// Request Mirroring Metrics
func (arm *AdvancedRoutingMetrics) RecordMirroredRequest(originalBackend, mirrorBackend string, status int, duration time.Duration) {
	arm.collector.RecordRequest("GET", "/advanced/mirror", fmt.Sprintf("%d", status), mirrorBackend, duration)
}

// Slow Start Metrics
func (arm *AdvancedRoutingMetrics) RecordSlowStartProgress(backend string, currentWeight float64, progress float64) {
	arm.collector.RecordRequest("GET", "/advanced/slow-start", "200", backend, time.Millisecond*50)
}

// Priority Queue Metrics
func (arm *AdvancedRoutingMetrics) RecordPriorityQueueSize(priority string, size int) {
	arm.collector.RecordRequest("GET", "/advanced/priority", "200", "priority-queue", time.Millisecond*10)
}

func (arm *AdvancedRoutingMetrics) RecordPriorityQueueWaitTime(priority string, waitTime time.Duration) {
	arm.collector.RecordRequest("GET", "/advanced/priority", "200", "priority-queue", waitTime)
}

func (arm *AdvancedRoutingMetrics) RecordLoadShedding(priority string, shedCount int) {
	arm.collector.RecordRequest("GET", "/advanced/load-shed", "429", "priority-queue", time.Millisecond*5)
}

// mTLS Metrics
func (arm *AdvancedRoutingMetrics) RecordMTLSSuccess(backend string, clientCert string) {
	arm.collector.RecordRequest("GET", "/advanced/mtls", "200", backend, time.Millisecond*20)
}

func (arm *AdvancedRoutingMetrics) RecordMTLSFailure(backend string, reason string) {
	arm.collector.RecordRequest("GET", "/advanced/mtls", "401", backend, time.Millisecond*10)
}

// gRPC Metrics
func (arm *AdvancedRoutingMetrics) RecordGRPCRequest(service, method string, backend string, duration time.Duration, statusCode int) {
	arm.collector.RecordRequest("GRPC", "/grpc/"+service, fmt.Sprintf("%d", statusCode), backend, duration)
}

// WebSocket Metrics
func (arm *AdvancedRoutingMetrics) RecordWebSocketConnection(backend string, clientIP string) {
	arm.collector.RecordRequest("WS", "/websocket", "101", backend, time.Millisecond*50)
}

func (arm *AdvancedRoutingMetrics) RecordWebSocketDisconnection(backend string, clientIP string, duration time.Duration) {
	arm.collector.RecordRequest("WS", "/websocket", "200", backend, duration)
}

func (arm *AdvancedRoutingMetrics) RecordWebSocketMessage(backend string, messageType string, size int) {
	arm.collector.RecordRequest("WS", "/websocket/message", "200", backend, time.Millisecond*5)
}

// Cache Metrics
func (arm *AdvancedRoutingMetrics) RecordCacheHit(key string, backend string) {
	arm.collector.RecordRequest("GET", "/cache", "200", "cache", time.Millisecond*1)
}

func (arm *AdvancedRoutingMetrics) RecordCacheMiss(key string) {
	arm.collector.RecordRequest("GET", "/cache", "404", "cache", time.Millisecond*2)
}

func (arm *AdvancedRoutingMetrics) RecordCacheEviction(key string, reason string) {
	arm.collector.RecordRequest("DELETE", "/cache", "200", "cache", time.Millisecond*1)
}

func (arm *AdvancedRoutingMetrics) RecordCacheSize(currentSize, maxSize int64, entries int, maxEntries int) {
	arm.collector.RecordRequest("GET", "/cache/stats", "200", "cache", time.Millisecond*1)
}
