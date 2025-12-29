// Package worker implements a worker pool for concurrent task processing.
// The worker pool pattern is fundamental in Go for controlling concurrency.
// It prevents resource exhaustion by limiting the number of concurrent
// operations while maximizing throughput.
//
// Key benefits of worker pools:
// - Controlled concurrency (prevents goroutine explosion)
// - Resource management (database connections, file handles)
// - Backpressure handling (queue builds up when workers are busy)
// - Graceful shutdown (finish in-flight work before exit)
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/smart-developer1791/go-task-processing-engine/internal/queue"
	"github.com/smart-developer1791/go-task-processing-engine/internal/storage"
	"github.com/smart-developer1791/go-task-processing-engine/internal/task"
	"github.com/smart-developer1791/go-task-processing-engine/pkg/metrics"
)

// Handler is a function that processes a specific task type.
// Handlers should be idempotent when possible - running the same
// task multiple times should produce the same result.
//
// The context should be respected for cancellation.
// Return an error to mark the task as failed.
type Handler func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)

// Pool manages a set of worker goroutines that process tasks.
// It coordinates work distribution, error handling, and shutdown.
type Pool struct {
	// size is the number of concurrent workers.
	size int

	// queue is the source of tasks to process.
	queue *queue.PriorityQueue

	// storage persists task state changes.
	storage storage.Storage

	// metrics collects processing statistics.
	metrics *metrics.Collector

	// logger for structured logging.
	logger *log.Logger

	// handlers maps task types to their processing functions.
	// Using sync.Map for thread-safe access without explicit locking.
	// Good for read-heavy workloads where the set of handlers rarely changes.
	handlers sync.Map

	// wg tracks running workers for graceful shutdown.
	// WaitGroup is the standard Go mechanism for waiting on
	// multiple goroutines to complete.
	wg sync.WaitGroup

	// running indicates if the pool is accepting new work.
	// Using atomic.Bool would be more performant for frequent checks,
	// but a regular bool with mutex is clearer for demonstration.
	running bool
	mu      sync.RWMutex
}

// NewPool creates a new worker pool.
// Workers are not started until Start() is called.
func NewPool(
	size int,
	queue *queue.PriorityQueue,
	storage storage.Storage,
	metrics *metrics.Collector,
	logger *log.Logger,
) *Pool {
	p := &Pool{
		size:    size,
		queue:   queue,
		storage: storage,
		metrics: metrics,
		logger:  logger,
	}

	// Register default handlers for demonstration.
	// In a real application, handlers would be registered by
	// the application during initialization.
	p.registerDefaultHandlers()

	return p
}

// registerDefaultHandlers adds built-in task handlers.
// These demonstrate different processing patterns.
func (p *Pool) registerDefaultHandlers() {
	// Echo handler: returns the input payload unchanged.
	// Useful for testing and as a minimal example.
	p.RegisterHandler("echo", func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		return payload, nil
	})

	// Sleep handler: simulates a long-running task.
	// Useful for testing timeout and cancellation.
	p.RegisterHandler("sleep", func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		var req struct {
			Duration string `json:"duration"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}

		duration, err := time.ParseDuration(req.Duration)
		if err != nil {
			return nil, fmt.Errorf("invalid duration: %w", err)
		}

		// Use select to respect context cancellation during sleep.
		// This is the proper way to implement cancellable delays.
		select {
		case <-time.After(duration):
			result := map[string]string{"slept_for": duration.String()}
			return json.Marshal(result)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})

	// Compute handler: simulates CPU-intensive work.
	// Demonstrates handling of compute-bound tasks.
	p.RegisterHandler("compute", func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		var req struct {
			Iterations int `json:"iterations"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}

		// Simulate computation with periodic cancellation checks.
		// For CPU-bound work, check context periodically to remain responsive.
		sum := 0
		for i := 0; i < req.Iterations; i++ {
			sum += i

			// Check for cancellation every 1000 iterations.
			// Balances responsiveness with performance overhead.
			if i%1000 == 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}
			}
		}

		result := map[string]int{"result": sum}
		return json.Marshal(result)
	})

	// Fail handler: always fails (for testing error handling).
	p.RegisterHandler("fail", func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		return nil, fmt.Errorf("intentional failure for testing")
	})
}

// RegisterHandler adds a handler for a specific task type.
// If a handler already exists for the type, it is replaced.
func (p *Pool) RegisterHandler(taskType string, handler Handler) {
	p.handlers.Store(taskType, handler)
}

// Start launches the worker goroutines.
// The context controls the lifetime of all workers.
// When the context is cancelled, workers finish their current
// task and then exit.
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()

	// Launch worker goroutines.
	// Each worker runs independently, pulling tasks from the shared queue.
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
}

// worker is the main loop for a single worker goroutine.
// It continuously pulls and processes tasks until shutdown.
func (p *Pool) worker(ctx context.Context, id int) {
	// Ensure we decrement the WaitGroup when the worker exits.
	// This is critical for proper shutdown coordination.
	defer p.wg.Done()

	p.logger.Printf("Worker %d started", id)

	for {
		// Check if shutdown has been requested.
		select {
		case <-ctx.Done():
			p.logger.Printf("Worker %d stopping: context cancelled", id)
			return
		default:
		}

		// Try to get the next task from the queue.
		// Pop blocks until a task is available or context is cancelled.
		t, err := p.queue.Pop(ctx)
		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				p.logger.Printf("Worker %d stopping: %v", id, err)
				return
			}
			if err == queue.ErrQueueClosed {
				p.logger.Printf("Worker %d stopping: queue closed", id)
				return
			}
			p.logger.Printf("Worker %d queue error: %v", id, err)
			continue
		}

		// Process the task with panic recovery.
		p.processTask(ctx, id, t)
	}
}

// processTask handles a single task with full error handling and metrics.
// This method is separated from the worker loop for clarity and testability.
func (p *Pool) processTask(ctx context.Context, workerID int, t *task.Task) {
	// Defer panic recovery to prevent a panicking handler from
	// crashing the entire worker pool.
	defer func() {
		if r := recover(); r != nil {
			// Log the panic with stack trace for debugging.
			stack := debug.Stack()
			p.logger.Printf("Worker %d panic processing task %s: %v\n%s",
				workerID, t.ID, r, stack)

			// Mark task as failed due to panic.
			t.MarkFailed(fmt.Errorf("panic: %v", r))
			p.storage.Update(t)
			p.metrics.RecordTaskFailed()
		}
	}()

	startTime := time.Now()

	// Update task status to running.
	t.MarkStarted()
	if err := p.storage.Update(t); err != nil {
		p.logger.Printf("Worker %d failed to update task %s: %v", workerID, t.ID, err)
	}

	// Look up the handler for this task type.
	handlerValue, ok := p.handlers.Load(t.Type)
	if !ok {
		// No handler registered for this task type.
		t.MarkFailed(fmt.Errorf("no handler registered for task type: %s", t.Type))
		p.storage.Update(t)
		p.metrics.RecordTaskFailed()
		return
	}

	handler := handlerValue.(Handler)

	// Execute the handler.
	// The handler receives a context that may have a deadline.
	result, err := handler(ctx, t.Payload)

	// Record processing duration.
	duration := time.Since(startTime)

	if err != nil {
		// Task failed - check if we should retry.
		if t.CanRetry() {
			t.IncrementRetry()
			p.storage.Update(t)

			// Re-queue the task for retry.
			// In production, you might implement exponential backoff
			// by delaying the re-queue.
			if pushErr := p.queue.Push(t); pushErr != nil {
				p.logger.Printf("Worker %d failed to requeue task %s: %v",
					workerID, t.ID, pushErr)
				t.MarkFailed(err)
				p.storage.Update(t)
				p.metrics.RecordTaskFailed()
			} else {
				p.logger.Printf("Worker %d requeued task %s (attempt %d/%d)",
					workerID, t.ID, t.RetryCount, t.MaxRetries)
			}
		} else {
			// No more retries - mark as failed.
			t.MarkFailed(err)
			p.storage.Update(t)
			p.metrics.RecordTaskFailed()
			p.logger.Printf("Worker %d task %s failed: %v", workerID, t.ID, err)
		}
	} else {
		// Task completed successfully.
		t.MarkCompleted(result)
		p.storage.Update(t)
		p.metrics.RecordTaskCompleted(duration)
		p.logger.Printf("Worker %d completed task %s in %v", workerID, t.ID, duration)
	}
}

// Wait blocks until all workers have exited.
// This should be called during shutdown after cancelling
// the context passed to Start().
func (p *Pool) Wait() {
	p.wg.Wait()
	p.logger.Println("All workers stopped")
}

// IsRunning returns whether the pool is currently processing tasks.
func (p *Pool) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// ActiveWorkers returns the number of workers that haven't exited yet.
// Note: This is an approximation based on the WaitGroup delta,
// which isn't directly accessible. In production, you'd use
// atomic counters for accurate tracking.
func (p *Pool) ActiveWorkers() int {
	// For accurate tracking, we'd need atomic counters.
	// This is a simplified implementation.
	return p.size
}
