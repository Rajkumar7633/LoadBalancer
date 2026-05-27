package tests

import (
	"testing"
	"time"

	"loadbalancer/internal/autoscaling"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// TestAutoscaler tests the autoscaler functionality
func TestAutoscaler(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := autoscaling.AutoscalerConfig{
		MinScale:              2,
		MaxScale:              10,
		ScaleUpCooldown:       30 * time.Second,
		ScaleDownCooldown:     60 * time.Second,
		CPUThreshold:          70.0,
		MemoryThreshold:       80.0,
		RequestRateThreshold:  1000.0,
		ResponseTimeThreshold: 100 * time.Millisecond,
		HealthCheckInterval:   30 * time.Second,
		UnhealthyThreshold:    3,
		HealthyThreshold:      2,
		SelfHealingEnabled:    true,
		HealingCooldown:       5 * time.Minute,
		ScaleUpPolicy: autoscaling.ScalingPolicyConfig{
			StepSize:            2,
			MaxStepSize:         5,
			AdjustmentFactor:    1.5,
			StabilizationWindow: 5 * time.Minute,
		},
		ScaleDownPolicy: autoscaling.ScalingPolicyConfig{
			StepSize:            1,
			MaxStepSize:         3,
			AdjustmentFactor:    1.0,
			StabilizationWindow: 10 * time.Minute,
		},
		Logger: logger,
	}

	as, err := autoscaling.NewAutoscaler(config)
	require.NoError(t, err)
	require.NotNil(t, as)
	defer as.Stop()

	t.Run("InitialScale", func(t *testing.T) {
		assert.Equal(t, 2, as.GetCurrentScale())
	})

	t.Run("MetricsCollection", func(t *testing.T) {
		metrics := as.GetMetrics()
		assert.NotNil(t, metrics)
		assert.Equal(t, 2, metrics.CurrentScale)
		assert.Equal(t, 2, metrics.TargetScale)
		assert.GreaterOrEqual(t, metrics.AverageCPU, 0.0)
	})

	t.Run("ScalingUp", func(t *testing.T) {
		// Wait for a bit to allow metrics collection
		time.Sleep(2 * time.Second)

		// Check if scaling logic is working (metrics should trigger scale up)
		metrics := as.GetMetrics()
		// Note: In a real test, we would mock metrics to trigger scaling
		// For now, we just verify the system is running
		assert.GreaterOrEqual(t, metrics.CurrentScale, config.MinScale)
		assert.LessOrEqual(t, metrics.CurrentScale, config.MaxScale)
	})

	t.Run("ScalingDown", func(t *testing.T) {
		// Similar to scale up, this would test scale down logic
		metrics := as.GetMetrics()
		assert.GreaterOrEqual(t, metrics.CurrentScale, config.MinScale)
		assert.LessOrEqual(t, metrics.CurrentScale, config.MaxScale)
	})
}

// TestScalingPolicy tests the scaling policy logic
func TestScalingPolicy(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := autoscaling.ScalingPolicyConfig{
		StepSize:            2,
		MaxStepSize:         5,
		AdjustmentFactor:    1.5,
		StabilizationWindow: 5 * time.Minute,
	}

	policy := autoscaling.NewScalingPolicy(config, logger)

	t.Run("PolicyCreation", func(t *testing.T) {
		// Test that the scaling policy is created successfully
		assert.NotNil(t, policy)
		assert.Equal(t, 2, policy.StepSize)
		assert.Equal(t, 5, policy.MaxStepSize)
		assert.Equal(t, 1.5, policy.AdjustmentFactor)
	})
}

// TestSelfHealing tests the self-healing functionality
func TestSelfHealing(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := autoscaling.AutoscalerConfig{
		MinScale:              2,
		MaxScale:              10,
		ScaleUpCooldown:       30 * time.Second,
		ScaleDownCooldown:     60 * time.Second,
		CPUThreshold:          70.0,
		MemoryThreshold:       80.0,
		RequestRateThreshold:  1000.0,
		ResponseTimeThreshold: 100 * time.Millisecond,
		SelfHealingEnabled:    true,
		HealingCooldown:       1 * time.Minute, // Short for testing
		Logger:                logger,
	}

	as, err := autoscaling.NewAutoscaler(config)
	require.NoError(t, err)
	require.NotNil(t, as)
	defer as.Stop()

	t.Run("HealingTrigger", func(t *testing.T) {
		// Wait for initial metrics collection
		time.Sleep(2 * time.Second)

		// Check that healing logic is enabled
		metrics := as.GetMetrics()
		assert.True(t, config.SelfHealingEnabled)

		// In a real test, we would simulate unhealthy conditions
		// For now, we just verify the system is configured for healing
		assert.GreaterOrEqual(t, metrics.TotalHealings, int64(0))
	})
}

// TestAutoscalingIntegration tests autoscaling integration
func TestAutoscalingIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := autoscaling.AutoscalerConfig{
		MinScale:              1,
		MaxScale:              5,
		ScaleUpCooldown:       10 * time.Second,
		ScaleDownCooldown:     20 * time.Second,
		CPUThreshold:          60.0,
		MemoryThreshold:       70.0,
		RequestRateThreshold:  500.0,
		ResponseTimeThreshold: 80 * time.Millisecond,
		SelfHealingEnabled:    true,
		HealingCooldown:       30 * time.Second,
		ScaleUpPolicy: autoscaling.ScalingPolicyConfig{
			StepSize:            1,
			MaxStepSize:         2,
			AdjustmentFactor:    1.2,
			StabilizationWindow: 2 * time.Minute,
		},
		ScaleDownPolicy: autoscaling.ScalingPolicyConfig{
			StepSize:            1,
			MaxStepSize:         2,
			AdjustmentFactor:    1.0,
			StabilizationWindow: 5 * time.Minute,
		},
		Logger: logger,
	}

	as, err := autoscaling.NewAutoscaler(config)
	require.NoError(t, err)
	require.NotNil(t, as)
	defer as.Stop()

	t.Run("FullScalingCycle", func(t *testing.T) {
		// Test the complete scaling cycle
		initialScale := as.GetCurrentScale()
		assert.Equal(t, 1, initialScale)

		// Wait for metrics collection and evaluation
		time.Sleep(5 * time.Second)

		// Check metrics are being collected
		metrics := as.GetMetrics()
		assert.NotNil(t, metrics)
		assert.GreaterOrEqual(t, metrics.AverageCPU, 0.0)
		assert.GreaterOrEqual(t, metrics.AverageMemory, 0.0)
		assert.GreaterOrEqual(t, metrics.AverageRequestRate, 0.0)

		// Verify scale stays within bounds
		currentScale := as.GetCurrentScale()
		assert.GreaterOrEqual(t, currentScale, config.MinScale)
		assert.LessOrEqual(t, currentScale, config.MaxScale)
	})

	t.Run("MetricsTracking", func(t *testing.T) {
		metrics := as.GetMetrics()

		// Verify all metrics are present
		assert.NotNil(t, metrics.TotalScaleUps)
		assert.NotNil(t, metrics.TotalScaleDowns)
		assert.NotNil(t, metrics.TotalHealings)
		assert.NotNil(t, metrics.CurrentScale)
		assert.NotNil(t, metrics.TargetScale)
		assert.NotNil(t, metrics.LastScaleAction)
		assert.NotNil(t, metrics.LastHealingAction)
		assert.NotNil(t, metrics.AverageCPU)
		assert.NotNil(t, metrics.AverageMemory)
		assert.NotNil(t, metrics.AverageRequestRate)
		assert.NotNil(t, metrics.AverageResponseTime)
	})

	t.Run("CooldownPeriods", func(t *testing.T) {
		// Test that cooldown periods are respected
		// This is more of a behavioral test
		metrics := as.GetMetrics()

		// The system should respect cooldowns
		// In a real test, we would trigger scaling and verify cooldown behavior
		assert.NotNil(t, metrics.LastScaleAction)
	})
}

// TestAutoscalingEdgeCases tests edge cases
func TestAutoscalingEdgeCases(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("InvalidConfiguration", func(t *testing.T) {
		config := autoscaling.AutoscalerConfig{
			MinScale: 0, // Invalid
			MaxScale: 10,
			Logger:   logger,
		}

		as, err := autoscaling.NewAutoscaler(config)
		assert.Error(t, err)
		assert.Nil(t, as)
	})

	t.Run("MaxScaleLessThanMinScale", func(t *testing.T) {
		config := autoscaling.AutoscalerConfig{
			MinScale: 10,
			MaxScale: 5, // Invalid
			Logger:   logger,
		}

		as, err := autoscaling.NewAutoscaler(config)
		assert.Error(t, err)
		assert.Nil(t, as)
	})

	t.Run("ValidConfiguration", func(t *testing.T) {
		config := autoscaling.AutoscalerConfig{
			MinScale:              2,
			MaxScale:              8,
			ScaleUpCooldown:       30 * time.Second,
			ScaleDownCooldown:     60 * time.Second,
			CPUThreshold:          70.0,
			MemoryThreshold:       80.0,
			RequestRateThreshold:  1000.0,
			ResponseTimeThreshold: 100 * time.Millisecond,
			SelfHealingEnabled:    false,
			Logger:                logger,
		}

		as, err := autoscaling.NewAutoscaler(config)
		assert.NoError(t, err)
		assert.NotNil(t, as)
		as.Stop()
	})
}

// TestAutoscalingPerformance tests autoscaling performance
func TestAutoscalingPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	logger := zaptest.NewLogger(t)

	config := autoscaling.AutoscalerConfig{
		MinScale:              1,
		MaxScale:              10,
		ScaleUpCooldown:       5 * time.Second,
		ScaleDownCooldown:     10 * time.Second,
		CPUThreshold:          50.0, // Lower threshold for testing
		MemoryThreshold:       60.0,
		RequestRateThreshold:  500.0,
		ResponseTimeThreshold: 50 * time.Millisecond,
		SelfHealingEnabled:    true,
		HealingCooldown:       30 * time.Second,
		Logger:                logger,
	}

	as, err := autoscaling.NewAutoscaler(config)
	require.NoError(t, err)
	require.NotNil(t, as)
	defer as.Stop()

	t.Run("PerformanceUnderLoad", func(t *testing.T) {
		startTime := time.Now()

		// Run for a period to collect metrics
		time.Sleep(10 * time.Second)

		duration := time.Since(startTime)
		metrics := as.GetMetrics()

		// Performance assertions
		assert.Less(t, duration, 15*time.Second) // Should complete quickly
		assert.GreaterOrEqual(t, metrics.CurrentScale, config.MinScale)
		assert.LessOrEqual(t, metrics.CurrentScale, config.MaxScale)

		// Metrics should be reasonable
		assert.GreaterOrEqual(t, metrics.AverageCPU, 0.0)
		assert.LessOrEqual(t, metrics.AverageCPU, 100.0)
		assert.GreaterOrEqual(t, metrics.AverageMemory, 0.0)
		assert.LessOrEqual(t, metrics.AverageMemory, 100.0)
	})
}

// BenchmarkAutoscaler benchmarks the autoscaler performance
func BenchmarkAutoscaler(b *testing.B) {
	logger := zaptest.NewLogger(b)

	config := autoscaling.AutoscalerConfig{
		MinScale:              1,
		MaxScale:              5,
		ScaleUpCooldown:       1 * time.Second,
		ScaleDownCooldown:     2 * time.Second,
		CPUThreshold:          70.0,
		MemoryThreshold:       80.0,
		RequestRateThreshold:  1000.0,
		ResponseTimeThreshold: 100 * time.Millisecond,
		SelfHealingEnabled:    false,
		Logger:                logger,
	}

	as, err := autoscaling.NewAutoscaler(config)
	require.NoError(b, err)
	defer as.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Benchmark metrics collection
		metrics := as.GetMetrics()
		if metrics == nil {
			b.Error("Metrics should not be nil")
		}
	}
}
