package core

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// WorkerPool represents a production-grade goroutine pool
type WorkerPool struct {
	workers     int
	taskQueue   chan Task
	workerQueue chan chan Task
	quit        chan bool
	wg          sync.WaitGroup
	mu          sync.RWMutex

	// Metrics
	activeWorkers  int32
	totalTasks     int64
	completedTasks int64
	failedTasks    int64

	// Configuration
	maxQueueSize  int
	workerTimeout time.Duration
	enableMetrics bool
	logger        *zap.Logger

	// Worker management
	workersList []*Worker
	workerPool  sync.Pool
}

// Task represents a unit of work
type Task struct {
	ID       string
	Function func() error
	Timeout  time.Duration
	Retry    int
	Context  context.Context
}

// Worker represents a single goroutine worker
type Worker struct {
	ID          int
	taskChannel chan Task
	quit        chan bool
	pool        *WorkerPool
	logger      *zap.Logger
}

// WorkerPoolConfig contains configuration for the worker pool
type WorkerPoolConfig struct {
	Workers       int
	MaxQueueSize  int
	WorkerTimeout time.Duration
	EnableMetrics bool
	Logger        *zap.Logger
}

// NewWorkerPool creates a new production-grade worker pool
func NewWorkerPool(config WorkerPoolConfig) *WorkerPool {
	if config.Workers <= 0 {
		config.Workers = runtime.NumCPU()
	}
	if config.MaxQueueSize <= 0 {
		config.MaxQueueSize = config.Workers * 100
	}
	if config.WorkerTimeout <= 0 {
		config.WorkerTimeout = 30 * time.Second
	}

	pool := &WorkerPool{
		workers:       config.Workers,
		taskQueue:     make(chan Task, config.MaxQueueSize),
		workerQueue:   make(chan chan Task, config.Workers),
		quit:          make(chan bool),
		maxQueueSize:  config.MaxQueueSize,
		workerTimeout: config.WorkerTimeout,
		enableMetrics: config.EnableMetrics,
		logger:        config.Logger,
		workersList:   make([]*Worker, 0, config.Workers),
	}

	// Initialize worker pool
	pool.workerPool = sync.Pool{
		New: func() interface{} {
			return &Worker{}
		},
	}

	return pool
}

// Start starts the worker pool
func (wp *WorkerPool) Start() {
	wp.logger.Info("Starting worker pool",
		zap.Int("workers", wp.workers),
		zap.Int("max_queue_size", wp.maxQueueSize))

	// Start workers
	for i := 0; i < wp.workers; i++ {
		worker := wp.createWorker(i)
		wp.workersList = append(wp.workersList, worker)
		wp.wg.Add(1)
		go worker.start()
	}

	// Start dispatcher
	wp.wg.Add(1)
	go wp.dispatcher()
}

// Stop gracefully stops the worker pool
func (wp *WorkerPool) Stop() {
	wp.logger.Info("Stopping worker pool")

	// Signal all workers to stop
	close(wp.quit)

	// Wait for all workers to finish
	done := make(chan struct{})
	go func() {
		wp.wg.Wait()
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		wp.logger.Info("Worker pool stopped gracefully")
	case <-time.After(30 * time.Second):
		wp.logger.Warn("Worker pool stop timeout")
	}
}

// Submit submits a task to the worker pool
func (wp *WorkerPool) Submit(task Task) error {
	if task.Context == nil {
		task.Context = context.Background()
	}
	if task.Timeout <= 0 {
		task.Timeout = wp.workerTimeout
	}

	select {
	case wp.taskQueue <- task:
		if wp.enableMetrics {
			atomic.AddInt64(&wp.totalTasks, 1)
		}
		return nil
	case <-task.Context.Done():
		return fmt.Errorf("task submission cancelled: %w", task.Context.Err())
	case <-time.After(5 * time.Second):
		return fmt.Errorf("task submission timeout")
	}
}

// SubmitWithPriority submits a task with priority (blocking)
func (wp *WorkerPool) SubmitWithPriority(task Task) error {
	if task.Context == nil {
		task.Context = context.Background()
	}
	if task.Timeout <= 0 {
		task.Timeout = wp.workerTimeout
	}

	select {
	case wp.taskQueue <- task:
		if wp.enableMetrics {
			atomic.AddInt64(&wp.totalTasks, 1)
		}
		return nil
	case <-task.Context.Done():
		return fmt.Errorf("task submission cancelled: %w", task.Context.Err())
	}
}

// createWorker creates a new worker
func (wp *WorkerPool) createWorker(id int) *Worker {
	worker := &Worker{
		ID:          id,
		taskChannel: make(chan Task),
		quit:        make(chan bool),
		pool:        wp,
		logger:      wp.logger,
	}
	return worker
}

// dispatcher dispatches tasks to available workers
func (wp *WorkerPool) dispatcher() {
	defer wp.wg.Done()

	for {
		select {
		case task := <-wp.taskQueue:
			go func() {
				// Get worker channel
				workerChannel := <-wp.workerQueue
				workerChannel <- task
			}()
		case <-wp.quit:
			return
		}
	}
}

// start starts the worker
func (w *Worker) start() {
	defer w.pool.wg.Done()

	w.logger.Debug("Starting worker", zap.Int("worker_id", w.ID))

	for {
		// Register worker channel
		w.pool.workerQueue <- w.taskChannel

		select {
		case task := <-w.taskChannel:
			atomic.AddInt32(&w.pool.activeWorkers, 1)
			w.executeTask(task)
			atomic.AddInt32(&w.pool.activeWorkers, -1)
		case <-w.quit:
			w.logger.Debug("Stopping worker", zap.Int("worker_id", w.ID))
			return
		}
	}
}

// executeTask executes a task with retry and timeout handling
func (w *Worker) executeTask(task Task) {
	ctx, cancel := context.WithTimeout(task.Context, task.Timeout)
	defer cancel()

	var err error
	for attempt := 0; attempt <= task.Retry; attempt++ {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break
		default:
			// Execute task
			func() {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("task panic: %v", r)
						w.logger.Error("Task panic recovered",
							zap.String("task_id", task.ID),
							zap.Any("panic", r))
					}
				}()

				err = task.Function()
			}()

			if err == nil {
				if w.pool.enableMetrics {
					atomic.AddInt64(&w.pool.completedTasks, 1)
				}
				w.logger.Debug("Task completed successfully",
					zap.String("task_id", task.ID),
					zap.Int("attempt", attempt))
				return
			}

			if attempt < task.Retry {
				w.logger.Warn("Task failed, retrying",
					zap.String("task_id", task.ID),
					zap.Int("attempt", attempt+1),
					zap.Error(err))

				// Exponential backoff
				backoff := time.Duration(attempt+1) * 100 * time.Millisecond
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					err = ctx.Err()
					break
				}
			}
		}
	}

	if err != nil {
		if w.pool.enableMetrics {
			atomic.AddInt64(&w.pool.failedTasks, 1)
		}
		w.logger.Error("Task failed after retries",
			zap.String("task_id", task.ID),
			zap.Error(err))
	}
}

// GetStats returns worker pool statistics
func (wp *WorkerPool) GetStats() WorkerPoolStats {
	wp.mu.RLock()
	defer wp.mu.RUnlock()

	return WorkerPoolStats{
		Workers:        wp.workers,
		ActiveWorkers:  atomic.LoadInt32(&wp.activeWorkers),
		TotalTasks:     atomic.LoadInt64(&wp.totalTasks),
		CompletedTasks: atomic.LoadInt64(&wp.completedTasks),
		FailedTasks:    atomic.LoadInt64(&wp.failedTasks),
		QueueSize:      len(wp.taskQueue),
		MaxQueueSize:   wp.maxQueueSize,
	}
}

// WorkerPoolStats contains worker pool statistics
type WorkerPoolStats struct {
	Workers        int   `json:"workers"`
	ActiveWorkers  int32 `json:"active_workers"`
	TotalTasks     int64 `json:"total_tasks"`
	CompletedTasks int64 `json:"completed_tasks"`
	FailedTasks    int64 `json:"failed_tasks"`
	QueueSize      int   `json:"queue_size"`
	MaxQueueSize   int   `json:"max_queue_size"`
}

// Resize dynamically resizes the worker pool
func (wp *WorkerPool) Resize(newSize int) {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if newSize <= 0 {
		newSize = runtime.NumCPU()
	}

	if newSize == wp.workers {
		return
	}

	wp.logger.Info("Resizing worker pool",
		zap.Int("old_size", wp.workers),
		zap.Int("new_size", newSize))

	if newSize > wp.workers {
		// Add workers
		for i := wp.workers; i < newSize; i++ {
			worker := wp.createWorker(i)
			wp.workersList = append(wp.workersList, worker)
			wp.wg.Add(1)
			go worker.start()
		}
	} else {
		// Remove workers
		for i := 0; i < wp.workers-newSize; i++ {
			if len(wp.workersList) > 0 {
				worker := wp.workersList[len(wp.workersList)-1]
				wp.workersList = wp.workersList[:len(wp.workersList)-1]
				close(worker.quit)
			}
		}
	}

	wp.workers = newSize
}

// Health check for the worker pool
func (wp *WorkerPool) Health() error {
	stats := wp.GetStats()

	// Check if worker pool is responsive
	if stats.Workers == 0 {
		return fmt.Errorf("no workers available")
	}

	// Check queue overflow
	if float64(stats.QueueSize)/float64(stats.MaxQueueSize) > 0.9 {
		return fmt.Errorf("worker pool queue nearly full: %d/%d", stats.QueueSize, stats.MaxQueueSize)
	}

	// Check failure rate
	if stats.TotalTasks > 100 {
		failureRate := float64(stats.FailedTasks) / float64(stats.TotalTasks)
		if failureRate > 0.1 {
			return fmt.Errorf("high task failure rate: %.2f%%", failureRate*100)
		}
	}

	return nil
}
