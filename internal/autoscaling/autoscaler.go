package autoscaling

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Autoscaler handles automatic scaling and self-healing
type Autoscaler struct {
	config AutoscalerConfig
	logger *zap.Logger

	// Scaling state
	currentScale      int
	minScale          int
	maxScale          int
	scaleUpCooldown   time.Duration
	scaleDownCooldown time.Duration
	lastScaleAction   time.Time

	// Metrics and monitoring
	metrics       *AutoscalerMetrics
	healthChecker *HealthChecker
	scalingPolicy *ScalingPolicy

	// Control channels
	stopChan  chan struct{}
	scaleChan chan ScaleEvent
	mu        sync.RWMutex
}

// AutoscalerConfig contains autoscaler configuration
type AutoscalerConfig struct {
	// Basic scaling settings
	MinScale          int           `json:"min_scale"`
	MaxScale          int           `json:"max_scale"`
	ScaleUpCooldown   time.Duration `json:"scale_up_cooldown"`
	ScaleDownCooldown time.Duration `json:"scale_down_cooldown"`

	// Metrics thresholds
	CPUThreshold          float64       `json:"cpu_threshold"`           // CPU usage percentage
	MemoryThreshold       float64       `json:"memory_threshold"`        // Memory usage percentage
	RequestRateThreshold  float64       `json:"request_rate_threshold"`  // Requests per second
	ResponseTimeThreshold time.Duration `json:"response_time_threshold"` // Response time

	// Health check settings
	HealthCheckInterval time.Duration `json:"health_check_interval"`
	UnhealthyThreshold  int           `json:"unhealthy_threshold"`
	HealthyThreshold    int           `json:"healthy_threshold"`

	// Scaling policy
	ScaleUpPolicy   ScalingPolicyConfig `json:"scale_up_policy"`
	ScaleDownPolicy ScalingPolicyConfig `json:"scale_down_policy"`

	// Self-healing
	SelfHealingEnabled bool          `json:"self_healing_enabled"`
	HealingCooldown    time.Duration `json:"healing_cooldown"`

	// Advanced features
	PredictiveScaling bool `json:"predictive_scaling"`
	MachineLearning   bool `json:"machine_learning"`

	// Logging
	Logger *zap.Logger `json:"-"`
}

// AutoscalerMetrics tracks autoscaler metrics
type AutoscalerMetrics struct {
	TotalScaleUps       int64         `json:"total_scale_ups"`
	TotalScaleDowns     int64         `json:"total_scale_downs"`
	TotalHealings       int64         `json:"total_healings"`
	CurrentScale        int           `json:"current_scale"`
	TargetScale         int           `json:"target_scale"`
	LastScaleAction     time.Time     `json:"last_scale_action"`
	LastHealingAction   time.Time     `json:"last_healing_action"`
	AverageCPU          float64       `json:"average_cpu"`
	AverageMemory       float64       `json:"average_memory"`
	AverageRequestRate  float64       `json:"average_request_rate"`
	AverageResponseTime time.Duration `json:"average_response_time"`
}

// ScaleEvent represents a scaling event
type ScaleEvent struct {
	Type      ScaleType              `json:"type"`
	FromScale int                    `json:"from_scale"`
	ToScale   int                    `json:"to_scale"`
	Reason    string                 `json:"reason"`
	Timestamp time.Time              `json:"timestamp"`
	Metrics   map[string]interface{} `json:"metrics"`
}

// ScaleType represents the type of scaling
type ScaleType string

const (
	ScaleUp   ScaleType = "scale_up"
	ScaleDown ScaleType = "scale_down"
	Heal      ScaleType = "heal"
)

// ScalingPolicyConfig contains scaling policy configuration
type ScalingPolicyConfig struct {
	StepSize            int           `json:"step_size"`
	MaxStepSize         int           `json:"max_step_size"`
	AdjustmentFactor    float64       `json:"adjustment_factor"`
	StabilizationWindow time.Duration `json:"stabilization_window"`
}

// ScalingPolicy implements scaling logic
type ScalingPolicy struct {
	config           ScalingPolicyConfig
	logger           *zap.Logger
	StepSize         int
	MaxStepSize      int
	AdjustmentFactor float64
}

// HealthChecker performs health checks
type HealthChecker struct {
	config AutoscalerConfig
	logger *zap.Logger
}

// SystemMetrics represents system metrics
type SystemMetrics struct {
	CPUUsage          float64       `json:"cpu_usage"`
	MemoryUsage       float64       `json:"memory_usage"`
	RequestRate       float64       `json:"request_rate"`
	ResponseTime      time.Duration `json:"response_time"`
	ErrorRate         float64       `json:"error_rate"`
	ActiveConnections int           `json:"active_connections"`
	Timestamp         time.Time     `json:"timestamp"`
}

// NewAutoscaler creates a new autoscaler
func NewAutoscaler(config AutoscalerConfig) (*Autoscaler, error) {
	if config.MinScale <= 0 {
		return nil, fmt.Errorf("min_scale must be positive")
	}
	if config.MaxScale <= config.MinScale {
		return nil, fmt.Errorf("max_scale must be greater than min_scale")
	}

	as := &Autoscaler{
		config:            config,
		logger:            config.Logger,
		currentScale:      config.MinScale,
		minScale:          config.MinScale,
		maxScale:          config.MaxScale,
		scaleUpCooldown:   config.ScaleUpCooldown,
		scaleDownCooldown: config.ScaleDownCooldown,
		metrics: &AutoscalerMetrics{
			CurrentScale:    config.MinScale,
			TargetScale:     config.MinScale,
			LastScaleAction: time.Now(),
		},
		stopChan:  make(chan struct{}),
		scaleChan: make(chan ScaleEvent, 100),
	}

	// Initialize components
	as.healthChecker = NewHealthChecker(config)
	as.scalingPolicy = NewScalingPolicy(config.ScaleUpPolicy, config.Logger)

	// Start autoscaler
	go as.start()

	as.logger.Info("Autoscaler initialized",
		zap.Int("min_scale", config.MinScale),
		zap.Int("max_scale", config.MaxScale),
		zap.Float64("cpu_threshold", config.CPUThreshold))

	return as, nil
}

// start starts the autoscaler
func (as *Autoscaler) start() {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			as.evaluateScaling()
		case event := <-as.scaleChan:
			as.handleScaleEvent(event)
		case <-as.stopChan:
			return
		}
	}
}

// evaluateScaling evaluates if scaling is needed
func (as *Autoscaler) evaluateScaling() {
	metrics := as.collectMetrics()
	as.updateMetrics(metrics)

	// Check if scaling is needed
	targetScale := as.calculateTargetScale(metrics)

	if targetScale != as.currentScale {
		as.proposeScale(targetScale, metrics)
	}

	// Check for self-healing opportunities
	if as.config.SelfHealingEnabled {
		as.checkSelfHealing(metrics)
	}
}

// collectMetrics collects system metrics
func (as *Autoscaler) collectMetrics() *SystemMetrics {
	// In a real implementation, this would collect actual metrics
	// For now, we'll simulate metrics

	return &SystemMetrics{
		CPUUsage:          65.0 + (float64(time.Now().Unix()%40) - 20), // Simulate CPU usage
		MemoryUsage:       70.0 + (float64(time.Now().Unix()%30) - 15), // Simulate memory usage
		RequestRate:       1000.0 + float64(time.Now().Unix()%500),     // Simulate request rate
		ResponseTime:      time.Duration(50+time.Now().Unix()%100) * time.Millisecond,
		ErrorRate:         0.01 + float64(time.Now().Unix()%5)/1000, // Simulate error rate
		ActiveConnections: 100 + int(time.Now().Unix()%200),
		Timestamp:         time.Now(),
	}
}

// updateMetrics updates autoscaler metrics
func (as *Autoscaler) updateMetrics(metrics *SystemMetrics) {
	as.mu.Lock()
	defer as.mu.Unlock()

	// Update running averages
	alpha := 0.1 // Smoothing factor
	as.metrics.AverageCPU = alpha*metrics.CPUUsage + (1-alpha)*as.metrics.AverageCPU
	as.metrics.AverageMemory = alpha*metrics.MemoryUsage + (1-alpha)*as.metrics.AverageMemory
	as.metrics.AverageRequestRate = alpha*metrics.RequestRate + (1-alpha)*as.metrics.AverageRequestRate
	as.metrics.AverageResponseTime = time.Duration(alpha*float64(metrics.ResponseTime) + (1-alpha)*float64(as.metrics.AverageResponseTime))
}

// calculateTargetScale calculates the target scale based on metrics
func (as *Autoscaler) calculateTargetScale(metrics *SystemMetrics) int {
	targetScale := as.currentScale

	// Scale up conditions
	scaleUp := false
	if metrics.CPUUsage > as.config.CPUThreshold {
		scaleUp = true
		as.logger.Info("CPU threshold exceeded, considering scale up",
			zap.Float64("cpu_usage", metrics.CPUUsage),
			zap.Float64("threshold", as.config.CPUThreshold))
	}

	if metrics.MemoryUsage > as.config.MemoryThreshold {
		scaleUp = true
		as.logger.Info("Memory threshold exceeded, considering scale up",
			zap.Float64("memory_usage", metrics.MemoryUsage),
			zap.Float64("threshold", as.config.MemoryThreshold))
	}

	if metrics.RequestRate > as.config.RequestRateThreshold {
		scaleUp = true
		as.logger.Info("Request rate threshold exceeded, considering scale up",
			zap.Float64("request_rate", metrics.RequestRate),
			zap.Float64("threshold", as.config.RequestRateThreshold))
	}

	if metrics.ResponseTime > as.config.ResponseTimeThreshold {
		scaleUp = true
		as.logger.Info("Response time threshold exceeded, considering scale up",
			zap.Duration("response_time", metrics.ResponseTime),
			zap.Duration("threshold", as.config.ResponseTimeThreshold))
	}

	// Scale down conditions
	scaleDown := false
	if metrics.CPUUsage < as.config.CPUThreshold*0.5 &&
		metrics.MemoryUsage < as.config.MemoryThreshold*0.5 &&
		metrics.RequestRate < as.config.RequestRateThreshold*0.5 {
		scaleDown = true
		as.logger.Info("All metrics below 50% threshold, considering scale down")
	}

	// Apply scaling policy
	if scaleUp {
		targetScale = as.scalingPolicy.calculateScaleUp(as.currentScale, as.maxScale, metrics)
	} else if scaleDown {
		targetScale = as.scalingPolicy.calculateScaleDown(as.currentScale, as.minScale, metrics)
	}

	return targetScale
}

// proposeScale proposes a scaling action
func (as *Autoscaler) proposeScale(targetScale int, metrics *SystemMetrics) {
	as.mu.Lock()
	defer as.mu.Unlock()

	// Check cooldowns
	now := time.Now()
	if targetScale > as.currentScale {
		if now.Sub(as.lastScaleAction) < as.scaleUpCooldown {
			as.logger.Debug("Scale up cooldown active, skipping",
				zap.Duration("remaining", as.scaleUpCooldown-now.Sub(as.lastScaleAction)))
			return
		}
	} else if targetScale < as.currentScale {
		if now.Sub(as.lastScaleAction) < as.scaleDownCooldown {
			as.logger.Debug("Scale down cooldown active, skipping",
				zap.Duration("remaining", as.scaleDownCooldown-now.Sub(as.lastScaleAction)))
			return
		}
	}

	// Create scale event
	event := ScaleEvent{
		Type:      ScaleUp,
		FromScale: as.currentScale,
		ToScale:   targetScale,
		Reason:    "metrics_based_scaling",
		Timestamp: now,
		Metrics: map[string]interface{}{
			"cpu_usage":     metrics.CPUUsage,
			"memory_usage":  metrics.MemoryUsage,
			"request_rate":  metrics.RequestRate,
			"response_time": metrics.ResponseTime,
		},
	}

	if targetScale < as.currentScale {
		event.Type = ScaleDown
	}

	as.scaleChan <- event
}

// handleScaleEvent handles a scaling event
func (as *Autoscaler) handleScaleEvent(event ScaleEvent) {
	as.mu.Lock()
	defer as.mu.Unlock()

	as.logger.Info("Executing scaling action",
		zap.String("type", string(event.Type)),
		zap.Int("from_scale", event.FromScale),
		zap.Int("to_scale", event.ToScale),
		zap.String("reason", event.Reason))

	// Execute scaling (in a real implementation, this would interact with the orchestration system)
	as.currentScale = event.ToScale
	as.lastScaleAction = event.Timestamp

	// Update metrics
	if event.Type == ScaleUp {
		as.metrics.TotalScaleUps++
	} else if event.Type == ScaleDown {
		as.metrics.TotalScaleDowns++
	}
	as.metrics.CurrentScale = as.currentScale
	as.metrics.TargetScale = as.currentScale
	as.metrics.LastScaleAction = event.Timestamp

	as.logger.Info("Scaling action completed",
		zap.Int("new_scale", as.currentScale))
}

// checkSelfHealing checks for self-healing opportunities
func (as *Autoscaler) checkSelfHealing(metrics *SystemMetrics) {
	// Check for unhealthy conditions
	if metrics.ErrorRate > 0.05 { // 5% error rate
		as.logger.Warn("High error rate detected, triggering self-healing",
			zap.Float64("error_rate", metrics.ErrorRate))
		as.triggerHealing("high_error_rate", metrics)
	}

	if metrics.ResponseTime > as.config.ResponseTimeThreshold*2 {
		as.logger.Warn("Very high response time detected, triggering self-healing",
			zap.Duration("response_time", metrics.ResponseTime))
		as.triggerHealing("high_response_time", metrics)
	}
}

// triggerHealing triggers a self-healing action
func (as *Autoscaler) triggerHealing(reason string, metrics *SystemMetrics) {
	as.mu.Lock()
	defer as.mu.Unlock()

	// Check healing cooldown
	if time.Since(as.metrics.LastHealingAction) < as.config.HealingCooldown {
		as.logger.Debug("Healing cooldown active, skipping",
			zap.Duration("remaining", as.config.HealingCooldown-time.Since(as.metrics.LastHealingAction)))
		return
	}

	as.logger.Info("Triggering self-healing",
		zap.String("reason", reason))

	// Create healing event
	event := ScaleEvent{
		Type:      Heal,
		FromScale: as.currentScale,
		ToScale:   as.currentScale, // Healing doesn't change scale
		Reason:    reason,
		Timestamp: time.Now(),
		Metrics: map[string]interface{}{
			"cpu_usage":     metrics.CPUUsage,
			"memory_usage":  metrics.MemoryUsage,
			"request_rate":  metrics.RequestRate,
			"response_time": metrics.ResponseTime,
			"error_rate":    metrics.ErrorRate,
		},
	}

	as.scaleChan <- event
	as.metrics.TotalHealings++
	as.metrics.LastHealingAction = time.Now()
}

// GetMetrics returns autoscaler metrics
func (as *Autoscaler) GetMetrics() *AutoscalerMetrics {
	as.mu.RLock()
	defer as.mu.RUnlock()
	return as.metrics
}

// GetCurrentScale returns the current scale
func (as *Autoscaler) GetCurrentScale() int {
	as.mu.RLock()
	defer as.mu.RUnlock()
	return as.currentScale
}

// Stop stops the autoscaler
func (as *Autoscaler) Stop() {
	close(as.stopChan)
	as.logger.Info("Autoscaler stopped")
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(config AutoscalerConfig) *HealthChecker {
	return &HealthChecker{
		config: config,
		logger: config.Logger,
	}
}

// NewScalingPolicy creates a new scaling policy
func NewScalingPolicy(config ScalingPolicyConfig, logger *zap.Logger) *ScalingPolicy {
	return &ScalingPolicy{
		config:           config,
		logger:           logger,
		StepSize:         config.StepSize,
		MaxStepSize:      config.MaxStepSize,
		AdjustmentFactor: config.AdjustmentFactor,
	}
}

// calculateScaleUp calculates the scale up target
func (sp *ScalingPolicy) calculateScaleUp(currentScale, maxScale int, metrics *SystemMetrics) int {
	// Simple step-based scaling
	stepSize := sp.config.StepSize

	// Adjust step size based on how much we're over threshold
	if metrics.CPUUsage > 80 {
		stepSize = int(float64(stepSize) * sp.config.AdjustmentFactor)
	}

	// Limit to max step size
	if stepSize > sp.config.MaxStepSize {
		stepSize = sp.config.MaxStepSize
	}

	targetScale := currentScale + stepSize
	if targetScale > maxScale {
		targetScale = maxScale
	}

	return targetScale
}

// calculateScaleDown calculates the scale down target
func (sp *ScalingPolicy) calculateScaleDown(currentScale, minScale int, metrics *SystemMetrics) int {
	// Conservative scale down
	stepSize := sp.config.StepSize / 2

	targetScale := currentScale - stepSize
	if targetScale < minScale {
		targetScale = minScale
	}

	return targetScale
}
