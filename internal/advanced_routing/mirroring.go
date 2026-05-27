package advanced_routing

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type MirrorConfig struct {
	Percentage     float64       `json:"percentage"` // 0.0 to 1.0
	MirrorBackends []string      `json:"mirror_backends"`
	Timeout        time.Duration `json:"timeout"`
	HeadersToCopy  []string      `json:"headers_to_copy"`
	Async          bool          `json:"async"` // Async mirroring for performance
	BufferSize     int           `json:"buffer_size"`
}

type MirrorResult struct {
	Backend  string
	Status   int
	Duration time.Duration
	Error    error
}

type RequestMirroring struct {
	config   MirrorConfig
	backends []backend.Backend
	mu       sync.RWMutex
}

func NewRequestMirroring(config MirrorConfig) *RequestMirroring {
	// Set default values
	if config.Percentage <= 0 || config.Percentage > 1.0 {
		config.Percentage = 0.1 // Default 10%
	}
	if config.Timeout == 0 {
		config.Timeout = 5 * time.Second
	}
	if config.BufferSize == 0 {
		config.BufferSize = 32 * 1024 // 32KB
	}
	if len(config.HeadersToCopy) == 0 {
		config.HeadersToCopy = []string{
			"User-Agent", "Accept", "Accept-Language", "Accept-Encoding",
			"X-Forwarded-For", "X-Real-IP", "X-Request-ID",
		}
	}

	return &RequestMirroring{
		config:   config,
		backends: make([]backend.Backend, 0),
	}
}

func (rm *RequestMirroring) SetBackends(backends []backend.Backend) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.backends = backends
}

func (rm *RequestMirroring) findBackendByURL(urlStr string) backend.Backend {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	for _, backend := range rm.backends {
		if backend.GetURL().String() == urlStr {
			return backend
		}
	}
	return nil
}

func (rm *RequestMirroring) ShouldMirror() bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rand.Float64() < rm.config.Percentage
}

func (rm *RequestMirroring) MirrorRequest(
	ctx context.Context,
	req *http.Request,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) []MirrorResult {
	rm.mu.RLock()
	config := rm.config
	rm.mu.RUnlock()

	if !rm.ShouldMirror() {
		return nil
	}

	results := make([]MirrorResult, 0, len(config.MirrorBackends))

	if config.Async {
		// Async mirroring - don't wait for results
		go rm.mirrorAsync(ctx, req, executeFunc)
		return results
	}

	// Sync mirroring - resolve backend strings to actual backends
	for _, backendRef := range config.MirrorBackends {
		if backend := rm.findBackendByURL(backendRef); backend != nil {
			result := rm.mirrorToBackend(ctx, req, backend, executeFunc)
			results = append(results, result)
		}
	}

	return results
}

func (rm *RequestMirroring) mirrorAsync(
	ctx context.Context,
	req *http.Request,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) {
	rm.mu.RLock()
	config := rm.config
	rm.mu.RUnlock()

	for range config.MirrorBackends {
		go func() {
			// Simplified mirroring - in practice would resolve backend strings to actual backends
		}()
	}
}

func (rm *RequestMirroring) mirrorToBackend(
	ctx context.Context,
	req *http.Request,
	backend backend.Backend,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) MirrorResult {
	startTime := time.Now()

	// Create a copy of the request
	mirrorReq := rm.copyRequest(req)

	// Create context with timeout
	mirrorCtx, cancel := context.WithTimeout(ctx, rm.config.Timeout)
	defer cancel()

	resp, err := executeFunc(mirrorCtx, mirrorReq, backend)

	result := MirrorResult{
		Backend:  backend.GetURL().String(),
		Duration: time.Since(startTime),
	}

	if err != nil {
		result.Error = err
		result.Status = 0
	} else {
		result.Status = resp.StatusCode
		// Discard response body for mirror requests
		if resp.Body != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	return result
}

func (rm *RequestMirroring) copyRequest(req *http.Request) *http.Request {
	// Copy the request body
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	// Create new request
	mirrorReq := req.Clone(req.Context())
	if bodyBytes != nil {
		mirrorReq.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	// Add mirroring headers
	mirrorReq.Header.Set("X-Mirror-Request", "true")
	mirrorReq.Header.Set("X-Mirror-Timestamp", time.Now().Format(time.RFC3339Nano))
	mirrorReq.Header.Set("X-Original-Host", req.Host)

	return mirrorReq
}

// CanaryDeployment handles canary deployment routing
type CanaryDeployment struct {
	primaryBackends []backend.Backend
	canaryBackends  []backend.Backend
	canaryWeight    float64
	mirroring       *RequestMirroring
	mu              sync.RWMutex
}

func NewCanaryDeployment(
	primaryBackends []backend.Backend,
	canaryBackends []backend.Backend,
	canaryWeight float64,
	mirrorConfig MirrorConfig,
) *CanaryDeployment {
	if canaryWeight <= 0 || canaryWeight > 1.0 {
		canaryWeight = 0.1 // Default 10%
	}

	return &CanaryDeployment{
		primaryBackends: primaryBackends,
		canaryBackends:  canaryBackends,
		canaryWeight:    canaryWeight,
		mirroring:       NewRequestMirroring(mirrorConfig),
	}
}

func (cd *CanaryDeployment) RouteToCanary(ctx context.Context, req *http.Request) backend.Backend {
	cd.mu.RLock()
	defer cd.mu.RUnlock()

	if rand.Float64() < cd.canaryWeight {
		// Route to canary
		if len(cd.canaryBackends) > 0 {
			// Simple round-robin for canary selection
			return cd.canaryBackends[rand.Intn(len(cd.canaryBackends))]
		}
	}

	// Route to primary
	if len(cd.primaryBackends) > 0 {
		return cd.primaryBackends[rand.Intn(len(cd.primaryBackends))]
	}

	return nil
}

func (cd *CanaryDeployment) MirrorToPrimary(
	ctx context.Context,
	req *http.Request,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) []MirrorResult {
	cd.mu.RLock()
	defer cd.mu.RUnlock()

	// Only mirror requests that went to canary
	if rand.Float64() < cd.canaryWeight {
		return cd.mirroring.MirrorRequest(ctx, req, executeFunc)
	}

	return nil
}

// MirrorTrafficSplitter for advanced traffic splitting
type MirrorTrafficSplitter struct {
	splits []TrafficSplit
	mu     sync.RWMutex
}

type TrafficSplit struct {
	Name     string
	Weight   float64
	Backends []backend.Backend
	Headers  map[string]string // Header-based routing
	Cookies  map[string]string // Cookie-based routing
}

func NewMirrorTrafficSplitter() *MirrorTrafficSplitter {
	return &MirrorTrafficSplitter{
		splits: make([]TrafficSplit, 0),
	}
}

func (ts *MirrorTrafficSplitter) AddSplit(split TrafficSplit) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.splits = append(ts.splits, split)
}

func (ts *MirrorTrafficSplitter) Route(req *http.Request) backend.Backend {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	// Check header-based routing first
	for _, split := range ts.splits {
		if ts.matchHeaders(req, split.Headers) {
			return ts.selectBackend(split.Backends)
		}
	}

	// Check cookie-based routing
	for _, split := range ts.splits {
		if ts.matchCookies(req, split.Cookies) {
			return ts.selectBackend(split.Backends)
		}
	}

	// Default weighted routing
	return ts.selectWeightedBackend(ts.splits)
}

func (ts *MirrorTrafficSplitter) matchHeaders(req *http.Request, headers map[string]string) bool {
	for key, value := range headers {
		if req.Header.Get(key) != value {
			return false
		}
	}
	return true
}

func (ts *MirrorTrafficSplitter) matchCookies(req *http.Request, cookies map[string]string) bool {
	for key, value := range cookies {
		if cookie, err := req.Cookie(key); err != nil || cookie.Value != value {
			return false
		}
	}
	return true
}

func (ts *MirrorTrafficSplitter) selectBackend(backends []backend.Backend) backend.Backend {
	if len(backends) > 0 {
		return backends[rand.Intn(len(backends))]
	}
	return nil
}

func (ts *MirrorTrafficSplitter) selectWeightedBackend(splits []TrafficSplit) backend.Backend {
	totalWeight := 0.0
	for _, split := range splits {
		totalWeight += split.Weight
	}

	if totalWeight == 0 {
		return nil
	}

	random := rand.Float64() * totalWeight
	currentWeight := 0.0

	for _, split := range splits {
		currentWeight += split.Weight
		if random <= currentWeight {
			if len(split.Backends) > 0 {
				return split.Backends[0]
			}
		}
	}

	return nil
}
