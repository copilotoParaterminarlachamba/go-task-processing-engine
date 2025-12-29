// Package main is the entry point of TaskForge - a concurrent task processing engine.
// This project demonstrates various Go concepts including:
// - Goroutines and channels for concurrency
// - Worker pool pattern for controlled parallelism
// - Priority queue using heap interface
// - Graceful shutdown with context cancellation
// - HTTP API with middleware pattern
// - Thread-safe operations with sync primitives
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/smart-developer1791/go-task-processing-engine/internal/api"
	"github.com/smart-developer1791/go-task-processing-engine/internal/queue"
	"github.com/smart-developer1791/go-task-processing-engine/internal/storage"
	"github.com/smart-developer1791/go-task-processing-engine/internal/worker"
	"github.com/smart-developer1791/go-task-processing-engine/pkg/metrics"
)

// Configuration constants for the application.
// In production, these would typically come from environment variables
// or a configuration file (e.g., YAML, TOML, or JSON).
const (
	// DefaultWorkerCount defines how many concurrent workers process tasks.
	// This should be tuned based on the nature of tasks:
	// - CPU-bound tasks: set to runtime.NumCPU()
	// - I/O-bound tasks: can be higher (e.g., 10x CPU count)
	DefaultWorkerCount = 5

	// DefaultQueueCapacity sets the maximum number of pending tasks.
	// When the queue is full, new task submissions will block or fail
	// depending on the chosen backpressure strategy.
	DefaultQueueCapacity = 1000

	// ServerAddress is the HTTP server binding address.
	// Use "0.0.0.0:8080" to accept connections from any interface.
	ServerAddress = ":8080"

	// GracefulShutdownTimeout is the maximum time to wait for
	// in-flight requests and running tasks to complete during shutdown.
	GracefulShutdownTimeout = 30 * time.Second
)

func main() {
	// Initialize structured logger.
	// In production, consider using zerolog or zap for better performance
	// and structured logging capabilities (JSON output, log levels, etc.)
	logger := log.New(os.Stdout, "[TaskForge] ", log.LstdFlags|log.Lshortfile)
	logger.Println("Starting TaskForge engine...")

	// Create a root context that will be cancelled on shutdown signal.
	// This context propagates cancellation to all components,
	// enabling coordinated graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize metrics collector for observability.
	// Metrics are essential for monitoring system health,
	// debugging performance issues, and capacity planning.
	metricsCollector := metrics.NewCollector()

	// Create the priority queue for task scheduling.
	// The priority queue ensures high-priority tasks are processed first,
	// implementing a form of quality-of-service (QoS).
	taskQueue := queue.NewPriorityQueue(DefaultQueueCapacity)

	// Initialize in-memory storage for task state.
	// Note: This implementation loses data on restart.
	// For persistence, implement a database-backed storage.
	taskStorage := storage.NewMemoryStorage()

	// Create and start the worker pool.
	// Workers are the "engines" that execute tasks concurrently.
	// The pool pattern limits concurrency to prevent resource exhaustion.
	pool := worker.NewPool(
		DefaultWorkerCount,
		taskQueue,
		taskStorage,
		metricsCollector,
		logger,
	)

	// Start workers in the background.
	// Each worker runs in its own goroutine, pulling tasks from the queue.
	pool.Start(ctx)
	logger.Printf("Started %d workers", DefaultWorkerCount)

	// Initialize and start HTTP API server.
	// The server provides REST endpoints for task management
	// and exposes Prometheus-compatible metrics.
	server := api.NewServer(
		ServerAddress,
		taskQueue,
		taskStorage,
		metricsCollector,
		logger,
	)

	// Start server in a separate goroutine since ListenAndServe blocks.
	// This pattern is common for running multiple concurrent services.
	go func() {
		logger.Printf("HTTP server listening on %s", ServerAddress)
		if err := server.Start(); err != nil {
			// Note: http.ErrServerClosed is expected during graceful shutdown
			logger.Printf("Server error: %v", err)
		}
	}()

	// Wait for termination signal.
	// This is the "signal handling" pattern for graceful shutdown.
	// We listen for SIGINT (Ctrl+C) and SIGTERM (docker stop, k8s).
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive a signal.
	sig := <-sigChan
	logger.Printf("Received signal: %v. Initiating graceful shutdown...", sig)

	// Create a deadline for graceful shutdown.
	// If shutdown takes longer than this, we force-exit.
	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(),
		GracefulShutdownTimeout,
	)
	defer shutdownCancel()

	// Cancel the root context to signal all workers to stop.
	// Workers will finish their current task before exiting.
	cancel()

	// Shutdown HTTP server gracefully.
	// This stops accepting new connections and waits for existing ones.
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("Server shutdown error: %v", err)
	}

	// Wait for worker pool to drain.
	// This ensures all in-progress tasks complete before exit.
	pool.Wait()

	// Log final metrics before exit.
	stats := metricsCollector.GetStats()
	logger.Printf("Final stats: processed=%d, failed=%d, avg_latency=%v",
		stats.TasksProcessed, stats.TasksFailed, stats.AverageLatency)

	logger.Println("Shutdown complete. Goodbye!")
}
