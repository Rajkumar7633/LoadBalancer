package advanced_routing

import (
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type SlowStartConfig struct {
	Enabled         bool          `json:"enabled"`
	Duration        time.Duration `json:"duration"`
	InitialWeight   float64       `json:"initial_weight"`
	StepInterval    time.Duration `json:"step_interval"`
	WeightIncrement float64       `json:"weight_increment"`
}

type SlowStartBackend struct {
	backend        backend.Backend
	originalWeight int
	currentWeight  int
	startTime      time.Time
	lastUpdate     time.Time
	inSlowStart    bool
}

type SlowStartManager struct {
	config    SlowStartConfig
	backends  map[string]*SlowStartBackend
	mu        sync.RWMutex
	stopChan  chan struct{}
}

func NewSlowStartManager(config SlowStartConfig) *SlowStartManager {
	// Set default values
	if config.Duration == 0 {
		config.Duration = 5 * time.Minute
	}
	if config.InitialWeight <= 0 || config.InitialWeight > 1.0 {
		config.InitialWeight = 0.1 // Start at 10%
	}
	if config.StepInterval == 0 {
		config.StepInterval = 30 * time.Second
	}
	if config.WeightIncrement <= 0 || config.WeightIncrement > 1.0 {
		config.WeightIncrement = 0.1 // Increment by 10%
	}

	return &SlowStartManager{
		config:   config,
		backends: make(map[string]*SlowStartBackend),
		stopChan: make(chan struct{}),
	}
}

func (ssm *SlowStartManager) Start() {
	if !ssm.config.Enabled {
		return
	}

	go ssm.runSlowStart()
}

func (ssm *SlowStartManager) Stop() {
	close(ssm.stopChan)
}

func (ssm *SlowStartManager) AddBackend(backend backend.Backend) {
	if !ssm.config.Enabled {
		return
	}

	ssm.mu.Lock()
	defer ssm.mu.Unlock()

	key := backend.GetURL().String()
	if _, exists := ssm.backends[key]; !exists {
		ssm.backends[key] = &SlowStartBackend{
			backend:        backend,
			originalWeight: backend.GetWeight(),
			currentWeight:  int(float64(backend.GetWeight()) * ssm.config.InitialWeight),
			startTime:      time.Now(),
			lastUpdate:     time.Now(),
			inSlowStart:    true,
		}
	}
}

func (ssm *SlowStartManager) RemoveBackend(backend backend.Backend) {
	ssm.mu.Lock()
	defer ssm.mu.Unlock()

	key := backend.GetURL().String()
	delete(ssm.backends, key)
}

func (ssm *SlowStartManager) GetEffectiveWeight(backend backend.Backend) int {
	if !ssm.config.Enabled {
		return backend.GetWeight()
	}

	ssm.mu.RLock()
	defer ssm.mu.RUnlock()

	key := backend.GetURL().String()
	if ssBackend, exists := ssm.backends[key]; exists && ssBackend.inSlowStart {
		return ssBackend.currentWeight
	}

	return backend.GetWeight()
}

func (ssm *SlowStartManager) runSlowStart() {
	ticker := time.NewTicker(ssm.config.StepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ssm.stopChan:
			return
		case <-ticker.C:
			ssm.updateWeights()
		}
	}
}

func (ssm *SlowStartManager) updateWeights() {
	ssm.mu.Lock()
	defer ssm.mu.Unlock()

	now := time.Now()
	for key, ssBackend := range ssm.backends {
		if !ssBackend.inSlowStart {
			continue
		}

		// Check if slow start period is over
		if now.Sub(ssBackend.startTime) >= ssm.config.Duration {
			// Restore original weight
			ssBackend.currentWeight = ssBackend.originalWeight
			ssBackend.inSlowStart = false
			delete(ssm.backends, key)
			continue
		}

		// Update weight if enough time has passed
		if now.Sub(ssBackend.lastUpdate) >= ssm.config.StepInterval {
			increment := int(float64(ssBackend.originalWeight) * ssm.config.WeightIncrement)
			newWeight := ssBackend.currentWeight + increment

			// Don't exceed original weight
			if newWeight > ssBackend.originalWeight {
				newWeight = ssBackend.originalWeight
			}

			ssBackend.currentWeight = newWeight
			ssBackend.lastUpdate = now
		}
	}
}

func (ssm *SlowStartManager) GetSlowStartStats() map[string]SlowStartStats {
	ssm.mu.RLock()
	defer ssm.mu.RUnlock()

	stats := make(map[string]SlowStartStats)
	for key, ssBackend := range ssm.backends {
		progress := float64(ssBackend.currentWeight) / float64(ssBackend.originalWeight)
		elapsed := time.Since(ssBackend.startTime)
		
		stats[key] = SlowStartStats{
			OriginalWeight: ssBackend.originalWeight,
			CurrentWeight:  ssBackend.currentWeight,
			Progress:       progress,
			StartTime:      ssBackend.startTime,
			Elapsed:        elapsed,
			InSlowStart:    ssBackend.inSlowStart,
		}
	}

	return stats
}

type SlowStartStats struct {
	OriginalWeight int         `json:"original_weight"`
	CurrentWeight  int         `json:"current_weight"`
	Progress       float64     `json:"progress"`
	StartTime      time.Time   `json:"start_time"`
	Elapsed        time.Duration `json:"elapsed"`
	InSlowStart    bool        `json:"in_slow_start"`
}

// WeightedSlowStartRouter integrates slow start with weighted routing
type WeightedSlowStartRouter struct {
	backends       []backend.Backend
	slowStartMgr   *SlowStartManager
	mu             sync.RWMutex
}

func NewWeightedSlowStartRouter(slowStartConfig SlowStartConfig) *WeightedSlowStartRouter {
	return &WeightedSlowStartRouter{
		backends:     make([]backend.Backend, 0),
		slowStartMgr: NewSlowStartManager(slowStartConfig),
	}
}

func (wssr *WeightedSlowStartRouter) SetBackends(backends []backend.Backend) {
	wssr.mu.Lock()
	defer wssr.mu.Unlock()

	// Remove old backends from slow start
	for _, backend := range wssr.backends {
		wssr.slowStartMgr.RemoveBackend(backend)
	}

	wssr.backends = backends

	// Add new backends to slow start
	for _, backend := range backends {
		wssr.slowStartMgr.AddBackend(backend)
	}
}

func (wssr *WeightedSlowStartRouter) SelectBackend() backend.Backend {
	wssr.mu.RLock()
	defer wssr.mu.RUnlock()

	if len(wssr.backends) == 0 {
		return nil
	}

	// Get healthy backends
	healthyBackends := make([]backend.Backend, 0)
	for _, backend := range wssr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			healthyBackends = append(healthyBackends, backend)
		}
	}

	if len(healthyBackends) == 0 {
		return nil
	}

	// Create weighted list considering slow start
	weightedList := make([]backend.Backend, 0)
	for _, backend := range healthyBackends {
		effectiveWeight := wssr.slowStartMgr.GetEffectiveWeight(backend)
		for i := 0; i < effectiveWeight; i++ {
			weightedList = append(weightedList, backend)
		}
	}

	if len(weightedList) == 0 {
		return healthyBackends[0]
	}

	// Simple random selection from weighted list
	return weightedList[time.Now().Nanosecond()%len(weightedList)]
}

func (wssr *WeightedSlowStartRouter) Start() {
	wssr.slowStartMgr.Start()
}

func (wssr *WeightedSlowStartRouter) Stop() {
	wssr.slowStartMgr.Stop()
}

func (wssr *WeightedSlowStartRouter) GetSlowStartStats() map[string]SlowStartStats {
	return wssr.slowStartMgr.GetSlowStartStats()
}
