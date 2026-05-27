package advanced_routing

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"time"

	"loadbalancer/internal/backend"
)

type RetryConfig struct {
	MaxRetries      int           `json:"max_retries"`
	InitialDelay    time.Duration `json:"initial_delay"`
	MaxDelay        time.Duration `json:"max_delay"`
	Multiplier      float64       `json:"multiplier"`
	Jitter          bool          `json:"jitter"`
	RetryableStatus []int         `json:"retryable_status"`
	RetryableMethods []string     `json:"retryable_methods"`
}

type RetryResult struct {
	Response *http.Response
	Error    error
	Attempts int
	Duration time.Duration
}

type RetryEngine struct {
	config RetryConfig
}

func NewRetryEngine(config RetryConfig) *RetryEngine {
	// Set default values
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.InitialDelay == 0 {
		config.InitialDelay = 100 * time.Millisecond
	}
	if config.MaxDelay == 0 {
		config.MaxDelay = 5 * time.Second
	}
	if config.Multiplier == 0 {
		config.Multiplier = 2.0
	}
	if len(config.RetryableStatus) == 0 {
		config.RetryableStatus = []int{502, 503, 504, 429}
	}
	if len(config.RetryableMethods) == 0 {
		config.RetryableMethods = []string{"GET", "HEAD", "PUT", "DELETE", "OPTIONS", "TRACE"}
	}

	return &RetryEngine{
		config: config,
	}
}

func (re *RetryEngine) ExecuteWithRetry(
	ctx context.Context,
	req *http.Request,
	backend backend.Backend,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) *RetryResult {
	startTime := time.Now()
	var lastResponse *http.Response
	var lastError error
	attempts := 0

	for attempts <= re.config.MaxRetries {
		attempts++
		
		// Execute the request
		resp, err := executeFunc(ctx, req, backend)
		
		// Check if we should retry
		if !re.shouldRetry(req, resp, err, attempts) {
			return &RetryResult{
				Response: resp,
				Error:    err,
				Attempts: attempts,
				Duration: time.Since(startTime),
			}
		}

		lastResponse = resp
		lastError = err

		// Don't wait after the last attempt
		if attempts > re.config.MaxRetries {
			break
		}

		// Calculate delay for next attempt
		delay := re.calculateDelay(attempts - 1)
		
		// Wait for delay or context cancellation
		select {
		case <-ctx.Done():
			return &RetryResult{
				Response: lastResponse,
				Error:    ctx.Err(),
				Attempts: attempts,
				Duration: time.Since(startTime),
			}
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	return &RetryResult{
		Response: lastResponse,
		Error:    lastError,
		Attempts: attempts,
		Duration: time.Since(startTime),
	}
}

func (re *RetryEngine) shouldRetry(req *http.Request, resp *http.Response, err error, attempts int) bool {
	// Don't retry if we've exceeded max retries
	if attempts > re.config.MaxRetries {
		return false
	}

	// Retry on network errors
	if err != nil {
		return true
	}

	// Check if method is retryable
	methodRetryable := false
	for _, method := range re.config.RetryableMethods {
		if req.Method == method {
			methodRetryable = true
			break
		}
	}
	if !methodRetryable {
		return false
	}

	// Check if status code is retryable
	for _, status := range re.config.RetryableStatus {
		if resp.StatusCode == status {
			return true
		}
	}

	return false
}

func (re *RetryEngine) calculateDelay(attempt int) time.Duration {
	// Exponential backoff: delay = initial_delay * multiplier^attempt
	delay := float64(re.config.InitialDelay) * math.Pow(re.config.Multiplier, float64(attempt))
	
	// Apply maximum delay limit
	if delay > float64(re.config.MaxDelay) {
		delay = float64(re.config.MaxDelay)
	}

	// Add jitter if enabled
	if re.config.Jitter {
		// Add random jitter up to ±25% of the delay
		jitterRange := delay * 0.25
		jitter := (rand.Float64()*2 - 1) * jitterRange
		delay += jitter
	}

	// Ensure delay is not negative
	if delay < 0 {
		delay = 0
	}

	return time.Duration(delay)
}

// Advanced retry strategies

type RetryStrategy interface {
	ShouldRetry(attempt int, resp *http.Response, err error) bool
	GetDelay(attempt int) time.Duration
}

type ExponentialBackoffStrategy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	Jitter       bool
}

func (s *ExponentialBackoffStrategy) ShouldRetry(attempt int, resp *http.Response, err error) bool {
	if attempt >= 3 { // Default max retries
		return false
	}
	
	if err != nil {
		return true
	}
	
	// Retry on server errors and rate limiting
	return resp.StatusCode >= 500 || resp.StatusCode == 429
}

func (s *ExponentialBackoffStrategy) GetDelay(attempt int) time.Duration {
	delay := float64(s.InitialDelay) * math.Pow(s.Multiplier, float64(attempt))
	
	if delay > float64(s.MaxDelay) {
		delay = float64(s.MaxDelay)
	}

	if s.Jitter {
		jitterRange := delay * 0.25
		jitter := (rand.Float64()*2 - 1) * jitterRange
		delay += jitter
	}

	return time.Duration(delay)
}

type LinearBackoffStrategy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Increment    time.Duration
	Jitter       bool
}

func (s *LinearBackoffStrategy) ShouldRetry(attempt int, resp *http.Response, err error) bool {
	if attempt >= 3 {
		return false
	}
	
	if err != nil {
		return true
	}
	
	return resp.StatusCode >= 500 || resp.StatusCode == 429
}

func (s *LinearBackoffStrategy) GetDelay(attempt int) time.Duration {
	delay := time.Duration(attempt) * s.Increment + s.InitialDelay
	
	if delay > s.MaxDelay {
		delay = s.MaxDelay
	}

	if s.Jitter {
		jitterRange := float64(delay) * 0.25
		jitter := (rand.Float64()*2 - 1) * jitterRange
		delay += time.Duration(jitter)
	}

	return delay
}

type CustomRetryEngine struct {
	strategy RetryStrategy
}

func NewCustomRetryEngine(strategy RetryStrategy) *CustomRetryEngine {
	return &CustomRetryEngine{
		strategy: strategy,
	}
}

func (cre *CustomRetryEngine) ExecuteWithRetry(
	ctx context.Context,
	req *http.Request,
	backend backend.Backend,
	executeFunc func(context.Context, *http.Request, backend.Backend) (*http.Response, error),
) *RetryResult {
	startTime := time.Now()
	attempts := 0
	var lastResponse *http.Response
	var lastError error

	for cre.strategy.ShouldRetry(attempts, lastResponse, lastError) {
		attempts++
		
		resp, err := executeFunc(ctx, req, backend)
		
		if !cre.strategy.ShouldRetry(attempts, resp, err) {
			return &RetryResult{
				Response: resp,
				Error:    err,
				Attempts: attempts,
				Duration: time.Since(startTime),
			}
		}

		lastResponse = resp
		lastError = err

		delay := cre.strategy.GetDelay(attempts)
		select {
		case <-ctx.Done():
			return &RetryResult{
				Response: lastResponse,
				Error:    ctx.Err(),
				Attempts: attempts,
				Duration: time.Since(startTime),
			}
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	return &RetryResult{
		Response: lastResponse,
		Error:    lastError,
		Attempts: attempts,
		Duration: time.Since(startTime),
	}
}
