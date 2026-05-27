package advanced_routing

import (
	"container/heap"
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type Priority int

const (
	PriorityFree     Priority = 0
	PriorityBasic    Priority = 1
	PriorityPremium  Priority = 2
	PriorityVIP      Priority = 3
	PriorityInternal Priority = 4
)

type PriorityRequest struct {
	RequestID   string
	Priority    Priority
	ArrivalTime time.Time
	ExpiryTime  time.Time
	Backend     backend.Backend
	Index       int // For heap interface
}

type PriorityQueue struct {
	requests []*PriorityRequest
	mu       sync.RWMutex
}

func NewPriorityQueue() *PriorityQueue {
	pq := &PriorityQueue{
		requests: make([]*PriorityRequest, 0),
	}
	heap.Init(pq)
	return pq
}

func (pq *PriorityQueue) Len() int { return len(pq.requests) }

func (pq *PriorityQueue) Less(i, j int) bool {
	// Higher priority first
	if pq.requests[i].Priority != pq.requests[j].Priority {
		return pq.requests[i].Priority > pq.requests[j].Priority
	}
	// If same priority, earlier arrival time first
	return pq.requests[i].ArrivalTime.Before(pq.requests[j].ArrivalTime)
}

func (pq *PriorityQueue) Swap(i, j int) {
	pq.requests[i], pq.requests[j] = pq.requests[j], pq.requests[i]
	pq.requests[i].Index = i
	pq.requests[j].Index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(pq.requests)
	item := x.(*PriorityRequest)
	item.Index = n
	pq.requests = append(pq.requests, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := pq.requests
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.Index = -1 // for safety
	pq.requests = old[0 : n-1]
	return item
}

func (pq *PriorityQueue) Enqueue(request *PriorityRequest) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	heap.Push(pq, request)
}

func (pq *PriorityQueue) Dequeue() *PriorityRequest {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if len(pq.requests) == 0 {
		return nil
	}

	return heap.Pop(pq).(*PriorityRequest)
}

func (pq *PriorityQueue) RemoveExpired() []*PriorityRequest {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	now := time.Now()
	expired := make([]*PriorityRequest, 0)
	remaining := make([]*PriorityRequest, 0)

	for _, req := range pq.requests {
		if now.After(req.ExpiryTime) {
			expired = append(expired, req)
		} else {
			remaining = append(remaining, req)
		}
	}

	pq.requests = remaining
	heap.Init(pq)
	return expired
}

func (pq *PriorityQueue) Size() int {
	pq.mu.RLock()
	defer pq.mu.RUnlock()
	return len(pq.requests)
}

type PriorityConfig struct {
	Enabled         bool                `json:"enabled"`
	QueueSize       int                 `json:"queue_size"`
	ExpiryTime      time.Duration       `json:"expiry_time"`
	PriorityHeaders map[string]Priority `json:"priority_headers"`
	PriorityPaths   map[string]Priority `json:"priority_paths"`
	DefaultPriority Priority            `json:"default_priority"`
	LoadShedding    LoadSheddingConfig  `json:"load_shedding"`
}

type LoadSheddingConfig struct {
	Enabled        bool     `json:"enabled"`
	Threshold      float64  `json:"threshold"`       // CPU/memory threshold
	ShedPriority   Priority `json:"shed_priority"`   // Priority to start shedding
	ShedPercentage float64  `json:"shed_percentage"` // Percentage to shed
}

type PriorityRouter struct {
	backends      []backend.Backend
	priorityQueue *PriorityQueue
	config        PriorityConfig
	mu            sync.RWMutex
	stats         PriorityStats
}

type PriorityStats struct {
	TotalRequests     int64         `json:"total_requests"`
	ProcessedRequests int64         `json:"processed_requests"`
	RejectedRequests  int64         `json:"rejected_requests"`
	LoadShedRequests  int64         `json:"load_shed_requests"`
	QueueSize         int           `json:"queue_size"`
	AverageWaitTime   time.Duration `json:"average_wait_time"`
	LastUpdated       time.Time     `json:"last_updated"`
}

func NewPriorityRouter(config PriorityConfig) *PriorityRouter {
	// Set default values
	if config.QueueSize == 0 {
		config.QueueSize = 1000
	}
	if config.ExpiryTime == 0 {
		config.ExpiryTime = 30 * time.Second
	}
	if config.DefaultPriority == 0 {
		config.DefaultPriority = PriorityBasic
	}
	if config.PriorityHeaders == nil {
		config.PriorityHeaders = map[string]Priority{
			"X-Priority-Free":     PriorityFree,
			"X-Priority-Basic":    PriorityBasic,
			"X-Priority-Premium":  PriorityPremium,
			"X-Priority-VIP":      PriorityVIP,
			"X-Priority-Internal": PriorityInternal,
		}
	}
	if config.PriorityPaths == nil {
		config.PriorityPaths = map[string]Priority{
			"/health":    PriorityFree,
			"/api/v1/":   PriorityBasic,
			"/api/v2/":   PriorityPremium,
			"/admin/":    PriorityVIP,
			"/internal/": PriorityInternal,
		}
	}

	return &PriorityRouter{
		backends:      make([]backend.Backend, 0),
		priorityQueue: NewPriorityQueue(),
		config:        config,
		stats:         PriorityStats{LastUpdated: time.Now()},
	}
}

func (pr *PriorityRouter) SetBackends(backends []backend.Backend) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.backends = backends
}

func (pr *PriorityRouter) Route(requestID string, headers map[string][]string, path string) backend.Backend {
	if !pr.config.Enabled {
		return pr.selectBackend()
	}

	// Determine priority
	priority := pr.determinePriority(headers, path)

	// Check for load shedding
	if pr.shouldLoadShed(priority) {
		pr.stats.RejectedRequests++
		return nil
	}

	// Check queue size
	if pr.priorityQueue.Size() >= pr.config.QueueSize {
		// Try to remove expired requests first
		expired := pr.priorityQueue.RemoveExpired()
		if len(expired) == 0 && pr.priorityQueue.Size() >= pr.config.QueueSize {
			// Queue is full, reject request
			pr.stats.RejectedRequests++
			return nil
		}
	}

	// Create priority request
	request := &PriorityRequest{
		RequestID:   requestID,
		Priority:    priority,
		ArrivalTime: time.Now(),
		ExpiryTime:  time.Now().Add(pr.config.ExpiryTime),
	}

	// Add to queue
	pr.priorityQueue.Enqueue(request)
	pr.stats.TotalRequests++
	pr.stats.QueueSize = pr.priorityQueue.Size()

	// Try to process queue
	return pr.processQueue()
}

func (pr *PriorityRouter) determinePriority(headers map[string][]string, path string) Priority {
	// Check headers first
	for header, priority := range pr.config.PriorityHeaders {
		if values, exists := headers[header]; exists && len(values) > 0 {
			return priority
		}
	}

	// Check paths
	for pathPrefix, priority := range pr.config.PriorityPaths {
		if len(path) >= len(pathPrefix) && path[:len(pathPrefix)] == pathPrefix {
			return priority
		}
	}

	return pr.config.DefaultPriority
}

func (pr *PriorityRouter) shouldLoadShed(priority Priority) bool {
	if !pr.config.LoadShedding.Enabled {
		return false
	}

	// Simple load shedding based on priority
	// In a real implementation, this would check actual system metrics
	if priority <= pr.config.LoadShedding.ShedPriority {
		// Randomly shed some percentage of low-priority requests
		return time.Now().UnixNano()%100 < int64(pr.config.LoadShedding.ShedPercentage*100)
	}

	return false
}

func (pr *PriorityRouter) processQueue() backend.Backend {
	// Get next request from queue
	request := pr.priorityQueue.Dequeue()
	if request == nil {
		return nil
	}

	// Select backend for the request
	backend := pr.selectBackend()
	if backend == nil {
		return nil
	}

	// Update stats
	pr.stats.ProcessedRequests++
	pr.stats.QueueSize = pr.priorityQueue.Size()
	pr.stats.LastUpdated = time.Now()

	return backend
}

func (pr *PriorityRouter) selectBackend() backend.Backend {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	if len(pr.backends) == 0 {
		return nil
	}

	// Simple round-robin for now
	// In a real implementation, this would use the configured routing algorithm
	for _, backend := range pr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}

	return nil
}

func (pr *PriorityRouter) GetStats() PriorityStats {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	stats := pr.stats
	stats.QueueSize = pr.priorityQueue.Size()
	return stats
}

func (pr *PriorityRouter) StartCleanup() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			pr.priorityQueue.RemoveExpired()
		}
	}()
}

// TieredBackendManager manages backends by priority tier
type TieredBackendManager struct {
	tiers map[Priority][]backend.Backend
	mu    sync.RWMutex
}

func NewTieredBackendManager() *TieredBackendManager {
	return &TieredBackendManager{
		tiers: make(map[Priority][]backend.Backend),
	}
}

func (tbm *TieredBackendManager) AddBackend(priority Priority, backend backend.Backend) {
	tbm.mu.Lock()
	defer tbm.mu.Unlock()

	tbm.tiers[priority] = append(tbm.tiers[priority], backend)
}

func (tbm *TieredBackendManager) RemoveBackend(priority Priority, backend backend.Backend) {
	tbm.mu.Lock()
	defer tbm.mu.Unlock()

	backends := tbm.tiers[priority]
	for i, b := range backends {
		if b.GetURL().String() == backend.GetURL().String() {
			tbm.tiers[priority] = append(backends[:i], backends[i+1:]...)
			break
		}
	}
}

func (tbm *TieredBackendManager) GetBackend(priority Priority) backend.Backend {
	tbm.mu.RLock()
	defer tbm.mu.RUnlock()

	backends := tbm.tiers[priority]
	if len(backends) == 0 {
		return nil
	}

	// Simple round-robin selection
	for _, backend := range backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}

	return nil
}

func (tbm *TieredBackendManager) GetAnyBackend() backend.Backend {
	tbm.mu.RLock()
	defer tbm.mu.RUnlock()

	// Try from highest to lowest priority
	priorities := []Priority{PriorityInternal, PriorityVIP, PriorityPremium, PriorityBasic, PriorityFree}

	for _, priority := range priorities {
		if backend := tbm.GetBackend(priority); backend != nil {
			return backend
		}
	}

	return nil
}
