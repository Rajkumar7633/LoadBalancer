package advanced_routing

import (
	"context"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type AdaptiveRouter struct {
	backends      []backend.Backend
	mlEngine      *MLEngine
	healthMonitor *HealthMonitor
	config        AdaptiveConfig
	mu            sync.RWMutex
}

type AdaptiveConfig struct {
	Enabled             bool          `json:"enabled"`
	Algorithm           string        `json:"algorithm"` // "weighted_round_robin", "least_response_time", "predictive"
	WindowSize          int           `json:"window_size"`
	UpdateInterval      time.Duration `json:"update_interval"`
	ResponseTimeWeight  float64       `json:"response_time_weight"`
	ErrorRateWeight     float64       `json:"error_rate_weight"`
	ConnectionWeight    float64       `json:"connection_weight"`
	PredictionHorizon   time.Duration `json:"prediction_horizon"`
	MinConfidence       float64       `json:"min_confidence"`
	AdaptationThreshold float64       `json:"adaptation_threshold"`
}

type BackendMetrics struct {
	URL             string        `json:"url"`
	ResponseTime    time.Duration `json:"response_time"`
	ErrorRate       float64       `json:"error_rate"`
	ConnectionCount int           `json:"connection_count"`
	Throughput      float64       `json:"throughput"`
	Availability    float64       `json:"availability"`
	LastUpdated     time.Time     `json:"last_updated"`
	PredictedLoad   float64       `json:"predicted_load"`
	Confidence      float64       `json:"confidence"`
	Weight          float64       `json:"weight"`
}

type MLEngine struct {
	models       map[string]*MLModel
	trainingData []TrainingSample
	config       MLConfig
	mu           sync.RWMutex
}

type MLModel struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"` // "linear_regression", "neural_network", "random_forest"
	Weights     []float64 `json:"weights"`
	Bias        float64   `json:"bias"`
	Accuracy    float64   `json:"accuracy"`
	LastTrained time.Time `json:"last_trained"`
	Predictions int       `json:"predictions"`
}

type TrainingSample struct {
	Features  []float64 `json:"features"`
	Target    float64   `json:"target"`
	Timestamp time.Time `json:"timestamp"`
}

type MLConfig struct {
	ModelType        string        `json:"model_type"`
	TrainingInterval time.Duration `json:"training_interval"`
	MaxSamples       int           `json:"max_samples"`
	LearningRate     float64       `json:"learning_rate"`
	Epochs           int           `json:"epochs"`
}

type HealthMonitor struct {
	metrics map[string]*BackendMetrics
	mu      sync.RWMutex
}

func NewAdaptiveRouter(config AdaptiveConfig) (*AdaptiveRouter, error) {
	mlConfig := MLConfig{
		ModelType:        "linear_regression",
		TrainingInterval: time.Minute * 5,
		MaxSamples:       10000,
		LearningRate:     0.01,
		Epochs:           100,
	}

	return &AdaptiveRouter{
		backends:      make([]backend.Backend, 0),
		mlEngine:      NewMLEngine(mlConfig),
		healthMonitor: NewHealthMonitor(),
		config:        config,
	}, nil
}

func (ar *AdaptiveRouter) SetBackends(backends []backend.Backend) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	ar.backends = backends

	// Initialize metrics for new backends
	for _, backend := range backends {
		ar.healthMonitor.InitializeMetrics(backend.GetURL().String())
	}
}

func (ar *AdaptiveRouter) Route(ctx context.Context, r *http.Request) backend.Backend {
	ar.mu.RLock()
	defer ar.mu.RUnlock()

	switch ar.config.Algorithm {
	case "weighted_round_robin":
		return ar.weightedRoundRobin()
	case "least_response_time":
		return ar.leastResponseTime()
	case "predictive":
		return ar.predictiveRouting()
	default:
		return ar.weightedRoundRobin()
	}
}

func (ar *AdaptiveRouter) weightedRoundRobin() backend.Backend {
	metrics := ar.healthMonitor.GetAllMetrics()

	// Calculate weights based on multiple factors
	for _, metric := range metrics {
		metric.Weight = ar.calculateWeight(metric)
	}

	// Sort by weight (highest first)
	sort.Slice(metrics, func(i, j int) bool {
		return metrics[i].Weight > metrics[j].Weight
	})

	// Find the first healthy backend
	for _, metric := range metrics {
		if backend := ar.findBackendByURL(metric.URL); backend != nil {
			if backend.IsHealthy() && !backend.IsDraining() {
				return backend
			}
		}
	}

	return ar.selectHealthyBackend()
}

func (ar *AdaptiveRouter) leastResponseTime() backend.Backend {
	metrics := ar.healthMonitor.GetAllMetrics()

	var bestBackend *BackendMetrics
	for _, metric := range metrics {
		if bestBackend == nil || metric.ResponseTime < bestBackend.ResponseTime {
			bestBackend = metric
		}
	}

	if bestBackend != nil {
		if backend := ar.findBackendByURL(bestBackend.URL); backend != nil {
			if backend.IsHealthy() && !backend.IsDraining() {
				return backend
			}
		}
	}

	return ar.selectHealthyBackend()
}

func (ar *AdaptiveRouter) predictiveRouting() backend.Backend {
	metrics := ar.healthMonitor.GetAllMetrics()

	// Use ML predictions to select best backend
	var bestBackend *BackendMetrics
	var bestScore float64 = -1

	for _, metric := range metrics {
		// Get prediction from ML engine
		prediction, confidence := ar.mlEngine.PredictLoad(metric)
		metric.PredictedLoad = prediction
		metric.Confidence = confidence

		// Calculate score based on prediction and confidence
		if confidence >= ar.config.MinConfidence {
			score := ar.calculatePredictiveScore(metric)
			if score > bestScore {
				bestScore = score
				bestBackend = metric
			}
		}
	}

	if bestBackend != nil {
		if backend := ar.findBackendByURL(bestBackend.URL); backend != nil {
			if backend.IsHealthy() && !backend.IsDraining() {
				return backend
			}
		}
	}

	// Fallback to weighted round robin
	return ar.weightedRoundRobin()
}

func (ar *AdaptiveRouter) calculateWeight(metric *BackendMetrics) float64 {
	// Normalize metrics
	responseTimeScore := 1.0 / math.Max(float64(metric.ResponseTime.Milliseconds()), 1)
	errorRateScore := 1.0 - metric.ErrorRate
	connectionScore := 1.0 / math.Max(float64(metric.ConnectionCount), 1)
	availabilityScore := metric.Availability

	// Calculate weighted score
	weight := (responseTimeScore * ar.config.ResponseTimeWeight) +
		(errorRateScore * ar.config.ErrorRateWeight) +
		(connectionScore * ar.config.ConnectionWeight) +
		(availabilityScore * 0.2) // Fixed weight for availability

	return weight
}

func (ar *AdaptiveRouter) calculatePredictiveScore(metric *BackendMetrics) float64 {
	// Score based on predicted load and current performance
	predictedLoadScore := 1.0 / math.Max(metric.PredictedLoad, 1)
	currentPerformanceScore := ar.calculateWeight(metric)

	// Weight by confidence
	return (predictedLoadScore*0.6 + currentPerformanceScore*0.4) * metric.Confidence
}

func (ar *AdaptiveRouter) findBackendByURL(url string) backend.Backend {
	for _, backend := range ar.backends {
		if backend.GetURL().String() == url {
			return backend
		}
	}
	return nil
}

func (ar *AdaptiveRouter) selectHealthyBackend() backend.Backend {
	for _, backend := range ar.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}
	return nil
}

func (ar *AdaptiveRouter) UpdateMetrics(backendURL string, responseTime time.Duration, isError bool) {
	ar.healthMonitor.UpdateMetrics(backendURL, responseTime, isError)
}

func (ar *AdaptiveRouter) StartAdaptiveLearning() {
	ticker := time.NewTicker(ar.config.UpdateInterval)
	go func() {
		for range ticker.C {
			ar.mlEngine.Train(ar.healthMonitor.GetTrainingData())
		}
	}()
}

func (ar *AdaptiveRouter) GetStats() AdaptiveStats {
	ar.mu.RLock()
	defer ar.mu.RUnlock()

	return AdaptiveStats{
		Algorithm:     ar.config.Algorithm,
		BackendsCount: len(ar.backends),
		ModelsTrained: ar.mlEngine.GetTrainedModelsCount(),
		LastUpdate:    time.Now(),
	}
}

type AdaptiveStats struct {
	Algorithm     string    `json:"algorithm"`
	BackendsCount int       `json:"backends_count"`
	ModelsTrained int       `json:"models_trained"`
	LastUpdate    time.Time `json:"last_update"`
}

// Health Monitor implementation
func NewHealthMonitor() *HealthMonitor {
	return &HealthMonitor{
		metrics: make(map[string]*BackendMetrics),
	}
}

func (hm *HealthMonitor) InitializeMetrics(backendURL string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if _, exists := hm.metrics[backendURL]; !exists {
		hm.metrics[backendURL] = &BackendMetrics{
			URL:          backendURL,
			Availability: 1.0,
			LastUpdated:  time.Now(),
		}
	}
}

func (hm *HealthMonitor) UpdateMetrics(backendURL string, responseTime time.Duration, isError bool) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	metric, exists := hm.metrics[backendURL]
	if !exists {
		metric = &BackendMetrics{
			URL:          backendURL,
			Availability: 1.0,
			LastUpdated:  time.Now(),
		}
		hm.metrics[backendURL] = metric
	}

	// Update metrics with exponential moving average
	alpha := 0.1 // Smoothing factor
	metric.ResponseTime = time.Duration(float64(metric.ResponseTime)*(1-alpha) + float64(responseTime)*alpha)

	if isError {
		metric.ErrorRate = metric.ErrorRate*(1-alpha) + alpha
	} else {
		metric.ErrorRate = metric.ErrorRate * (1 - alpha)
	}

	metric.LastUpdated = time.Now()
}

func (hm *HealthMonitor) GetAllMetrics() []*BackendMetrics {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	metrics := make([]*BackendMetrics, 0, len(hm.metrics))
	for _, metric := range hm.metrics {
		metrics = append(metrics, metric)
	}
	return metrics
}

func (hm *HealthMonitor) GetTrainingData() []TrainingSample {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	// Convert metrics to training samples
	samples := make([]TrainingSample, 0)
	for _, metric := range hm.metrics {
		features := []float64{
			float64(metric.ResponseTime.Milliseconds()),
			metric.ErrorRate,
			float64(metric.ConnectionCount),
			metric.Throughput,
			metric.Availability,
		}
		samples = append(samples, TrainingSample{
			Features:  features,
			Target:    metric.PredictedLoad,
			Timestamp: metric.LastUpdated,
		})
	}
	return samples
}

// ML Engine implementation
func NewMLEngine(config MLConfig) *MLEngine {
	return &MLEngine{
		models:       make(map[string]*MLModel),
		trainingData: make([]TrainingSample, 0),
		config:       config,
	}
}

func (ml *MLEngine) PredictLoad(metric *BackendMetrics) (float64, float64) {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	model, exists := ml.models["load_prediction"]
	if !exists {
		return 0.5, 0.0 // No model available
	}

	features := []float64{
		float64(metric.ResponseTime.Milliseconds()),
		metric.ErrorRate,
		float64(metric.ConnectionCount),
		metric.Throughput,
		metric.Availability,
	}

	prediction := ml.predict(model, features)
	confidence := math.Min(model.Accuracy, 0.95) // Cap confidence at 95%

	return prediction, confidence
}

func (ml *MLEngine) predict(model *MLModel, features []float64) float64 {
	if len(features) != len(model.Weights) {
		return 0.5 // Default prediction
	}

	var sum float64
	for i, feature := range features {
		sum += feature * model.Weights[i]
	}
	sum += model.Bias

	// Apply sigmoid activation for normalization
	return 1.0 / (1.0 + math.Exp(-sum))
}

func (ml *MLEngine) Train(samples []TrainingSample) {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	if len(samples) < 10 {
		return // Not enough data
	}

	// Create or update model
	model, exists := ml.models["load_prediction"]
	if !exists {
		model = &MLModel{
			Name:    "load_prediction",
			Type:    ml.config.ModelType,
			Weights: make([]float64, 5), // 5 features
			Bias:    0.0,
		}
		ml.models["load_prediction"] = model
	}

	// Simple gradient descent training
	ml.gradientDescent(model, samples)
	model.LastTrained = time.Now()
}

func (ml *MLEngine) gradientDescent(model *MLModel, samples []TrainingSample) {
	learningRate := ml.config.LearningRate
	epochs := ml.config.Epochs

	for epoch := 0; epoch < epochs; epoch++ {
		totalError := 0.0

		for _, sample := range samples {
			prediction := ml.predict(model, sample.Features)
			error := prediction - sample.Target

			// Update weights
			for i, feature := range sample.Features {
				model.Weights[i] -= learningRate * error * feature
			}
			model.Bias -= learningRate * error

			totalError += error * error
		}

		// Calculate accuracy (inverse of mean squared error)
		mse := totalError / float64(len(samples))
		model.Accuracy = 1.0 / (1.0 + mse)
	}
}

func (ml *MLEngine) GetTrainedModelsCount() int {
	ml.mu.RLock()
	defer ml.mu.RUnlock()
	return len(ml.models)
}
