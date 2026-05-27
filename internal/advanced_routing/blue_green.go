package advanced_routing

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type BlueGreenRouter struct {
	deployment    *Deployment
	trafficSplit  *TrafficSplitter
	healthChecker *BlueGreenHealthChecker
	config        BlueGreenConfig
	mu            sync.RWMutex
}

type Deployment struct {
	Name          string            `json:"name"`
	Environment   string            `json:"environment"` // "blue" or "green"
	Version       string            `json:"version"`
	Status        DeploymentStatus  `json:"status"`
	BlueBackends  []backend.Backend `json:"blue_backends"`
	GreenBackends []backend.Backend `json:"green_backends"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type DeploymentStatus string

const (
	StatusActive      DeploymentStatus = "active"
	StatusInactive    DeploymentStatus = "inactive"
	StatusDeploying   DeploymentStatus = "deploying"
	StatusRollingBack DeploymentStatus = "rolling_back"
	StatusTesting     DeploymentStatus = "testing"
)

type TrafficSplitter struct {
	BluePercentage   float64       `json:"blue_percentage"`
	GreenPercentage  float64       `json:"green_percentage"`
	Mode             string        `json:"mode"` // "manual", "gradual", "automatic"
	TargetPercentage float64       `json:"target_percentage"`
	StepSize         float64       `json:"step_size"`
	StepInterval     time.Duration `json:"step_interval"`
	LastStep         time.Time     `json:"last_step"`
}

type BlueGreenConfig struct {
	Enabled             bool          `json:"enabled"`
	DefaultEnvironment  string        `json:"default_environment"`
	HealthCheckInterval time.Duration `json:"health_check_interval"`
	DeploymentTimeout   time.Duration `json:"deployment_timeout"`
	RollbackTimeout     time.Duration `json:"rollback_timeout"`
	AutoPromotion       bool          `json:"auto_promotion"`
	HealthThreshold     float64       `json:"health_threshold"`
	TrafficShiftMode    string        `json:"traffic_shift_mode"`
}

type BlueGreenHealthChecker struct {
	checks    map[string]*HealthCheck
	interval  time.Duration
	threshold float64
	mu        sync.RWMutex
}

type HealthCheck struct {
	BackendURL   string        `json:"backend_url"`
	Status       string        `json:"status"`
	ResponseTime time.Duration `json:"response_time"`
	ErrorRate    float64       `json:"error_rate"`
	LastCheck    time.Time     `json:"last_check"`
	Healthy      bool          `json:"healthy"`
}

type DeploymentPlan struct {
	Name           string              `json:"name"`
	TargetVersion  string              `json:"target_version"`
	Strategy       string              `json:"strategy"` // "blue_green", "canary", "rolling"
	TrafficShift   TrafficShift        `json:"traffic_shift"`
	HealthChecks   []HealthCheckConfig `json:"health_checks"`
	RollbackPolicy RollbackPolicy      `json:"rollback_policy"`
	CreatedAt      time.Time           `json:"created_at"`
}

type TrafficShift struct {
	Duration     time.Duration `json:"duration"`
	Steps        int           `json:"steps"`
	Increment    float64       `json:"increment"`
	Verification bool          `json:"verification"`
}

type HealthCheckConfig struct {
	Path             string        `json:"path"`
	Method           string        `json:"method"`
	ExpectedStatus   int           `json:"expected_status"`
	Timeout          time.Duration `json:"timeout"`
	Interval         time.Duration `json:"interval"`
	FailureThreshold int           `json:"failure_threshold"`
}

type RollbackPolicy struct {
	Enabled               bool          `json:"enabled"`
	ErrorThreshold        float64       `json:"error_threshold"`
	ResponseTimeThreshold time.Duration `json:"response_time_threshold"`
	Timeout               time.Duration `json:"timeout"`
	Automatic             bool          `json:"automatic"`
}

func NewBlueGreenRouter(config BlueGreenConfig) (*BlueGreenRouter, error) {
	healthChecker := &BlueGreenHealthChecker{
		checks:    make(map[string]*HealthCheck),
		interval:  config.HealthCheckInterval,
		threshold: config.HealthThreshold,
	}

	return &BlueGreenRouter{
		deployment:    nil,
		trafficSplit:  &TrafficSplitter{Mode: config.TrafficShiftMode},
		healthChecker: healthChecker,
		config:        config,
	}, nil
}

func (bgr *BlueGreenRouter) SetBackends(blueBackends, greenBackends []backend.Backend) {
	bgr.mu.Lock()
	defer bgr.mu.Unlock()

	if bgr.deployment == nil {
		bgr.deployment = &Deployment{
			Name:          "default",
			Environment:   bgr.config.DefaultEnvironment,
			Version:       "1.0.0",
			Status:        StatusActive,
			BlueBackends:  blueBackends,
			GreenBackends: greenBackends,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
	} else {
		bgr.deployment.BlueBackends = blueBackends
		bgr.deployment.GreenBackends = greenBackends
		bgr.deployment.UpdatedAt = time.Now()
	}

	// Initialize health checks for all backends
	bgr.initializeHealthChecks()
}

func (bgr *BlueGreenRouter) Route(ctx context.Context, r *http.Request) backend.Backend {
	bgr.mu.RLock()
	defer bgr.mu.RUnlock()

	if bgr.deployment == nil {
		return nil
	}

	// Determine which environment to route to
	environment := bgr.selectEnvironment()

	var backends []backend.Backend
	switch environment {
	case "blue":
		backends = bgr.deployment.BlueBackends
	case "green":
		backends = bgr.deployment.GreenBackends
	default:
		// Fallback to default environment
		if bgr.config.DefaultEnvironment == "blue" {
			backends = bgr.deployment.BlueBackends
		} else {
			backends = bgr.deployment.GreenBackends
		}
	}

	// Select healthy backend from chosen environment
	return bgr.selectHealthyBackend(backends)
}

func (bgr *BlueGreenRouter) selectEnvironment() string {
	// Check if we're in a transition state
	if bgr.deployment.Status == StatusDeploying || bgr.deployment.Status == StatusRollingBack {
		return bgr.selectTransitionEnvironment()
	}

	// Normal routing based on traffic split
	if rand.Float64() < bgr.trafficSplit.BluePercentage/100.0 {
		return "blue"
	}
	return "green"
}

func (bgr *BlueGreenRouter) selectTransitionEnvironment() string {
	// During deployment or rollback, gradually shift traffic
	if bgr.trafficSplit.Mode == "gradual" {
		bgr.updateGradualShift()
	}

	if rand.Float64() < bgr.trafficSplit.BluePercentage/100.0 {
		return "blue"
	}
	return "green"
}

func (bgr *BlueGreenRouter) updateGradualShift() {
	now := time.Now()
	if now.Sub(bgr.trafficSplit.LastStep) < bgr.trafficSplit.StepInterval {
		return
	}

	// Move one step towards target percentage
	current := bgr.trafficSplit.BluePercentage
	target := bgr.trafficSplit.TargetPercentage
	step := bgr.trafficSplit.StepSize

	if current < target {
		current = math.Min(current+step, target)
	} else if current > target {
		current = math.Max(current-step, target)
	}

	bgr.trafficSplit.BluePercentage = current
	bgr.trafficSplit.GreenPercentage = 100 - current
	bgr.trafficSplit.LastStep = now

	// Check if we've reached the target
	if current == target {
		if bgr.deployment.Status == StatusDeploying && current == 100 {
			bgr.completeDeployment()
		} else if bgr.deployment.Status == StatusRollingBack && current == 0 {
			bgr.completeRollback()
		}
	}
}

func (bgr *BlueGreenRouter) selectHealthyBackend(backends []backend.Backend) backend.Backend {
	for _, backend := range backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			// Additional blue-green health check
			if bgr.healthChecker.IsHealthy(backend.GetURL().String()) {
				return backend
			}
		}
	}
	return nil
}

func (bgr *BlueGreenRouter) StartDeployment(plan DeploymentPlan) error {
	bgr.mu.Lock()
	defer bgr.mu.Unlock()

	if bgr.deployment == nil {
		return fmt.Errorf("no deployment configured")
	}

	// Check if already deploying
	if bgr.deployment.Status == StatusDeploying {
		return fmt.Errorf("deployment already in progress")
	}

	// Initialize deployment
	bgr.deployment.Status = StatusDeploying
	bgr.deployment.Version = plan.TargetVersion
	bgr.deployment.UpdatedAt = time.Now()

	// Setup traffic shift
	bgr.trafficSplit.TargetPercentage = 100 // Target is full green
	bgr.trafficSplit.StepSize = plan.TrafficShift.Increment
	bgr.trafficSplit.StepInterval = plan.TrafficShift.Duration / time.Duration(plan.TrafficShift.Steps)
	bgr.trafficSplit.LastStep = time.Now()

	// Start health monitoring
	go bgr.monitorDeployment(plan)

	return nil
}

func (bgr *BlueGreenRouter) StartRollback() error {
	bgr.mu.Lock()
	defer bgr.mu.Unlock()

	if bgr.deployment == nil {
		return fmt.Errorf("no deployment configured")
	}

	// Check if already rolling back
	if bgr.deployment.Status == StatusRollingBack {
		return fmt.Errorf("rollback already in progress")
	}

	// Initialize rollback
	bgr.deployment.Status = StatusRollingBack
	bgr.deployment.UpdatedAt = time.Now()

	// Setup traffic shift back to blue
	bgr.trafficSplit.TargetPercentage = 0 // Target is full blue
	bgr.trafficSplit.StepSize = 10.0      // 10% steps
	bgr.trafficSplit.StepInterval = time.Minute * 2
	bgr.trafficSplit.LastStep = time.Now()

	return nil
}

func (bgr *BlueGreenRouter) PromoteEnvironment(environment string) error {
	bgr.mu.Lock()
	defer bgr.mu.Unlock()

	if bgr.deployment == nil {
		return fmt.Errorf("no deployment configured")
	}

	if environment != "blue" && environment != "green" {
		return fmt.Errorf("invalid environment: %s", environment)
	}

	// Immediately switch all traffic to specified environment
	if environment == "blue" {
		bgr.trafficSplit.BluePercentage = 100
		bgr.trafficSplit.GreenPercentage = 0
	} else {
		bgr.trafficSplit.BluePercentage = 0
		bgr.trafficSplit.GreenPercentage = 100
	}

	bgr.deployment.Environment = environment
	bgr.deployment.Status = StatusActive
	bgr.deployment.UpdatedAt = time.Now()

	return nil
}

func (bgr *BlueGreenRouter) monitorDeployment(plan DeploymentPlan) {
	ticker := time.NewTicker(bgr.config.HealthCheckInterval)
	defer ticker.Stop()

	deploymentTimeout := time.NewTimer(bgr.config.DeploymentTimeout)
	defer deploymentTimeout.Stop()

	for {
		select {
		case <-ticker.C:
			if bgr.checkDeploymentHealth(plan) {
				if bgr.config.AutoPromotion {
					bgr.PromoteEnvironment("green")
				}
				return
			}
		case <-deploymentTimeout.C:
			// Deployment timeout, trigger rollback
			bgr.StartRollback()
			return
		}
	}
}

func (bgr *BlueGreenRouter) checkDeploymentHealth(plan DeploymentPlan) bool {
	// Check health of green environment
	greenHealthy := true
	for _, backend := range bgr.deployment.GreenBackends {
		if !bgr.healthChecker.IsHealthy(backend.GetURL().String()) {
			greenHealthy = false
			break
		}
	}

	// Check error rates and response times
	metrics := bgr.healthChecker.GetAggregatedMetrics()
	if metrics.ErrorRate > bgr.config.HealthThreshold {
		greenHealthy = false
	}

	return greenHealthy
}

func (bgr *BlueGreenRouter) completeDeployment() {
	bgr.mu.Lock()
	defer bgr.mu.Unlock()

	bgr.deployment.Status = StatusActive
	bgr.deployment.Environment = "green"
	bgr.deployment.UpdatedAt = time.Now()
}

func (bgr *BlueGreenRouter) completeRollback() {
	bgr.mu.Lock()
	defer bgr.mu.Unlock()

	bgr.deployment.Status = StatusActive
	bgr.deployment.Environment = "blue"
	bgr.deployment.UpdatedAt = time.Now()
}

func (bgr *BlueGreenRouter) initializeHealthChecks() {
	bgr.healthChecker.mu.Lock()
	defer bgr.healthChecker.mu.Unlock()

	// Clear existing checks
	bgr.healthChecker.checks = make(map[string]*HealthCheck)

	// Add checks for blue backends
	for _, backend := range bgr.deployment.BlueBackends {
		url := backend.GetURL().String()
		bgr.healthChecker.checks[url] = &HealthCheck{
			BackendURL: url,
			Status:     "unknown",
			Healthy:    false,
			LastCheck:  time.Now(),
		}
	}

	// Add checks for green backends
	for _, backend := range bgr.deployment.GreenBackends {
		url := backend.GetURL().String()
		bgr.healthChecker.checks[url] = &HealthCheck{
			BackendURL: url,
			Status:     "unknown",
			Healthy:    false,
			LastCheck:  time.Now(),
		}
	}

	// Start health checking
	go bgr.healthChecker.Start()
}

func (bgr *BlueGreenRouter) GetDeploymentStatus() *Deployment {
	bgr.mu.RLock()
	defer bgr.mu.RUnlock()

	// Return a copy to avoid concurrent access issues
	if bgr.deployment == nil {
		return nil
	}

	copy := *bgr.deployment
	return &copy
}

func (bgr *BlueGreenRouter) GetTrafficSplit() *TrafficSplitter {
	bgr.mu.RLock()
	defer bgr.mu.RUnlock()

	copy := *bgr.trafficSplit
	return &copy
}

func (bgr *BlueGreenRouter) GetStats() BlueGreenStats {
	bgr.mu.RLock()
	defer bgr.mu.RUnlock()

	stats := BlueGreenStats{
		DeploymentName:     "",
		Environment:        "",
		Status:             "",
		BluePercentage:     0,
		GreenPercentage:    0,
		BlueBackendsCount:  0,
		GreenBackendsCount: 0,
	}

	if bgr.deployment != nil {
		stats.DeploymentName = bgr.deployment.Name
		stats.Environment = bgr.deployment.Environment
		stats.Status = string(bgr.deployment.Status)
		stats.BlueBackendsCount = len(bgr.deployment.BlueBackends)
		stats.GreenBackendsCount = len(bgr.deployment.GreenBackends)
	}

	if bgr.trafficSplit != nil {
		stats.BluePercentage = bgr.trafficSplit.BluePercentage
		stats.GreenPercentage = bgr.trafficSplit.GreenPercentage
	}

	return stats
}

type BlueGreenStats struct {
	DeploymentName     string  `json:"deployment_name"`
	Environment        string  `json:"environment"`
	Status             string  `json:"status"`
	BluePercentage     float64 `json:"blue_percentage"`
	GreenPercentage    float64 `json:"green_percentage"`
	BlueBackendsCount  int     `json:"blue_backends_count"`
	GreenBackendsCount int     `json:"green_backends_count"`
}

// BlueGreenHealthChecker implementation
func (bgc *BlueGreenHealthChecker) Start() {
	ticker := time.NewTicker(bgc.interval)
	defer ticker.Stop()

	for range ticker.C {
		bgc.performHealthChecks()
	}
}

func (bgc *BlueGreenHealthChecker) performHealthChecks() {
	bgc.mu.Lock()
	defer bgc.mu.Unlock()

	for _, check := range bgc.checks {
		// Perform health check (simplified)
		check.LastCheck = time.Now()
		check.Status = "healthy"
		check.Healthy = true
		check.ResponseTime = time.Millisecond * 50
		check.ErrorRate = 0.01
	}
}

func (bgc *BlueGreenHealthChecker) IsHealthy(backendURL string) bool {
	bgc.mu.RLock()
	defer bgc.mu.RUnlock()

	check, exists := bgc.checks[backendURL]
	if !exists {
		return false
	}
	return check.Healthy
}

func (bgc *BlueGreenHealthChecker) GetAggregatedMetrics() HealthMetrics {
	bgc.mu.RLock()
	defer bgc.mu.RUnlock()

	metrics := HealthMetrics{
		TotalChecks:     len(bgc.checks),
		HealthyCount:    0,
		ErrorRate:       0,
		AvgResponseTime: 0,
	}

	totalResponseTime := time.Duration(0)
	for _, check := range bgc.checks {
		if check.Healthy {
			metrics.HealthyCount++
		}
		metrics.ErrorRate += check.ErrorRate
		totalResponseTime += check.ResponseTime
	}

	if len(bgc.checks) > 0 {
		metrics.ErrorRate /= float64(len(bgc.checks))
		metrics.AvgResponseTime = totalResponseTime / time.Duration(len(bgc.checks))
	}

	return metrics
}

type HealthMetrics struct {
	TotalChecks     int           `json:"total_checks"`
	HealthyCount    int           `json:"healthy_count"`
	ErrorRate       float64       `json:"error_rate"`
	AvgResponseTime time.Duration `json:"avg_response_time"`
}
