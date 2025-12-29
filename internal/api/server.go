// Package api implements the HTTP API for TaskForge.
// This package demonstrates Go HTTP server patterns including:
// - RESTful endpoint design
// - Middleware composition
// - JSON request/response handling
// - Error handling and status codes
// - Request validation
// - Graceful shutdown
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/smart-developer1791/go-task-processing-engine/internal/queue"
	"github.com/smart-developer1791/go-task-processing-engine/internal/storage"
	"github.com/smart-developer1791/go-task-processing-engine/internal/task"
	"github.com/smart-developer1791/go-task-processing-engine/pkg/metrics"
)

// Server encapsulates the HTTP server and its dependencies.
// Using dependency injection (DI) makes the server testable
// and allows swapping implementations without code changes.
type Server struct {
	// server is the underlying HTTP server.
	server *http.Server

	// queue is where new tasks are submitted.
	queue *queue.PriorityQueue

	// storage is for task persistence and retrieval.
	storage storage.Storage

	// metrics collects request and system metrics.
	metrics *metrics.Collector

	// logger for structured logging.
	logger *log.Logger
}

// NewServer creates a new API server with the given dependencies.
// The server is not started until Start() is called.
func NewServer(
	addr string,
	queue *queue.PriorityQueue,
	storage storage.Storage,
	metrics *metrics.Collector,
	logger *log.Logger,
) *Server {
	s := &Server{
		queue:   queue,
		storage: storage,
		metrics: metrics,
		logger:  logger,
	}

	// Create router (mux) and register routes.
	// Using the standard library's ServeMux for simplicity.
	// For production, consider gorilla/mux or chi for more features.
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Wrap the mux with middleware chain.
	// Middleware are applied in reverse order (last added = first executed).
	handler := s.withMiddleware(mux)

	// Create the HTTP server with reasonable timeouts.
	// Timeouts are critical for preventing resource exhaustion
	// from slow or malicious clients.
	s.server = &http.Server{
		Addr:    addr,
		Handler: handler,

		// ReadTimeout covers the time from accepting the connection
		// to fully reading the request body.
		ReadTimeout: 10 * time.Second,

		// WriteTimeout covers the time from reading the request
		// to finishing writing the response.
		WriteTimeout: 30 * time.Second,

		// IdleTimeout is the time before an idle keep-alive
		// connection is closed.
		IdleTimeout: 60 * time.Second,

		// ReadHeaderTimeout limits how long to read request headers.
		// Prevents slowloris attacks.
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s
}

// registerRoutes sets up all API endpoints.
// Following REST conventions:
// - GET for retrieval (idempotent, safe)
// - POST for creation (not idempotent)
// - PUT/PATCH for updates (idempotent)
// - DELETE for removal (idempotent)
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Serve static files (UI)
	mux.HandleFunc("/", s.handleStatic)

	// Task endpoints
	mux.HandleFunc("/api/v1/tasks", s.handleTasks)
	mux.HandleFunc("/api/v1/tasks/", s.handleTaskByID)

	// Health check endpoint - used by load balancers and orchestrators
	mux.HandleFunc("/health", s.handleHealth)

	// Metrics endpoint - Prometheus-compatible
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Queue stats endpoint
	mux.HandleFunc("/api/v1/stats", s.handleStats)
}

// withMiddleware wraps the handler with common middleware.
// Middleware pattern allows cross-cutting concerns like logging,
// authentication, and metrics to be applied uniformly.
func (s *Server) withMiddleware(handler http.Handler) http.Handler {
	// Apply middleware in order (inside-out execution).
	// The order matters: logging -> metrics -> recovery -> cors -> handler
	handler = s.recoveryMiddleware(handler)   // Panic recovery (outermost)
	handler = s.loggingMiddleware(handler)    // Request logging
	handler = s.metricsMiddleware(handler)    // Request metrics
	handler = s.corsMiddleware(handler)       // CORS headers
	return handler
}

// loggingMiddleware logs information about each request.
// This implements the middleware pattern: wrap the handler,
// do something before/after, call the original handler.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap ResponseWriter to capture status code.
		// The standard ResponseWriter doesn't expose the status code
		// after WriteHeader is called.
		wrapped := &responseWrapper{ResponseWriter: w, status: http.StatusOK}

		// Call the next handler
		next.ServeHTTP(wrapped, r)

		// Log after request completes
		s.logger.Printf("%s %s %d %v",
			r.Method,
			r.URL.Path,
			wrapped.status,
			time.Since(start),
		)
	})
}

// metricsMiddleware records request metrics.
func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWrapper{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		s.metrics.RecordHTTPRequest(r.Method, r.URL.Path, wrapped.status, time.Since(start))
	})
}

// recoveryMiddleware catches panics and returns 500 errors.
// Without this, a panic would crash the entire server.
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				s.logger.Printf("Panic recovered: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware adds CORS headers for browser access.
// CORS (Cross-Origin Resource Sharing) is required when the API
// is accessed from a different domain than where it's hosted.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// responseWrapper wraps http.ResponseWriter to capture the status code.
type responseWrapper struct {
	http.ResponseWriter
	status int
}

// WriteHeader captures the status code before writing it.
func (w *responseWrapper) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// handleTasks handles POST /api/v1/tasks (create) and GET /api/v1/tasks (list).
// Multiple HTTP methods on the same path is a common REST pattern.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createTask(w, r)
	case http.MethodGet:
		s.listTasks(w, r)
	default:
		// Return 405 Method Not Allowed with Allow header
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// createTask handles POST /api/v1/tasks.
// Creates a new task and queues it for processing.
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	// Limit request body size to prevent memory exhaustion.
	// MaxBytesReader returns an error if the body exceeds the limit.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	// Parse JSON request body
	var req task.CreateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields() // Strict parsing - reject unknown fields

	if err := decoder.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Validate request
	if err := req.Validate(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Create task entity
	t := task.New(&req)

	// Persist to storage
	if err := s.storage.Create(t); err != nil {
		s.logger.Printf("Failed to create task: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to create task")
		return
	}

	// Add to processing queue
	if err := s.queue.Push(t); err != nil {
		if errors.Is(err, queue.ErrQueueFull) {
			s.writeError(w, http.StatusServiceUnavailable, "Queue is full, try again later")
			return
		}
		s.logger.Printf("Failed to queue task: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to queue task")
		return
	}

	s.metrics.RecordTaskCreated()

	// Return created task with 201 Created status
	s.writeJSON(w, http.StatusCreated, t)
}

// listTasks handles GET /api/v1/tasks.
// Returns a list of tasks with optional filtering.
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters for filtering
	query := r.URL.Query()
	filter := &storage.ListFilter{}

	// Parse status filter
	if status := query.Get("status"); status != "" {
		s := task.Status(status)
		filter.Status = &s
	}

	// Parse type filter
	if taskType := query.Get("type"); taskType != "" {
		filter.Type = &taskType
	}

	// Retrieve tasks from storage
	tasks, err := s.storage.List(filter)
	if err != nil {
		s.logger.Printf("Failed to list tasks: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to retrieve tasks")
		return
	}

	// Return tasks array (empty array if none found, not null)
	s.writeJSON(w, http.StatusOK, tasks)
}

// handleTaskByID handles requests to /api/v1/tasks/{id}.
// Extracts the task ID from the URL path.
func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	// Extract task ID from URL path.
	// Path format: /api/v1/tasks/{id}
	// Using strings.TrimPrefix is a simple approach; for complex routing,
	// consider a dedicated router like chi or gorilla/mux.
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "Task ID required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getTask(w, r, id)
	case http.MethodDelete:
		s.deleteTask(w, r, id)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getTask handles GET /api/v1/tasks/{id}.
func (s *Server) getTask(w http.ResponseWriter, r *http.Request, id string) {
	t, err := s.storage.Get(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "Task not found")
			return
		}
		s.logger.Printf("Failed to get task: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to retrieve task")
		return
	}

	s.writeJSON(w, http.StatusOK, t)
}

// deleteTask handles DELETE /api/v1/tasks/{id}.
func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request, id string) {
	err := s.storage.Delete(id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, "Task not found")
			return
		}
		s.logger.Printf("Failed to delete task: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to delete task")
		return
	}

	// 204 No Content is the standard response for successful DELETE
	w.WriteHeader(http.StatusNoContent)
}

// handleHealth handles GET /health.
// Health endpoints are used by load balancers and container orchestrators
// to determine if the service is ready to receive traffic.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Simple health check - could be extended to check dependencies
	response := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"queue": map[string]interface{}{
			"size":   s.queue.Len(),
			"closed": s.queue.IsClosed(),
		},
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleMetrics handles GET /metrics.
// Returns Prometheus-compatible metrics output.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats := s.metrics.GetStats()

	// Output in Prometheus exposition format
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP taskforge_tasks_created_total Total number of tasks created\n")
	fmt.Fprintf(w, "# TYPE taskforge_tasks_created_total counter\n")
	fmt.Fprintf(w, "taskforge_tasks_created_total %d\n\n", stats.TasksCreated)

	fmt.Fprintf(w, "# HELP taskforge_tasks_processed_total Total number of tasks processed\n")
	fmt.Fprintf(w, "# TYPE taskforge_tasks_processed_total counter\n")
	fmt.Fprintf(w, "taskforge_tasks_processed_total %d\n\n", stats.TasksProcessed)

	fmt.Fprintf(w, "# HELP taskforge_tasks_failed_total Total number of tasks failed\n")
	fmt.Fprintf(w, "# TYPE taskforge_tasks_failed_total counter\n")
	fmt.Fprintf(w, "taskforge_tasks_failed_total %d\n\n", stats.TasksFailed)

	fmt.Fprintf(w, "# HELP taskforge_queue_size Current queue size\n")
	fmt.Fprintf(w, "# TYPE taskforge_queue_size gauge\n")
	fmt.Fprintf(w, "taskforge_queue_size %d\n\n", s.queue.Len())

	fmt.Fprintf(w, "# HELP taskforge_task_processing_seconds Average task processing time\n")
	fmt.Fprintf(w, "# TYPE taskforge_task_processing_seconds gauge\n")
	fmt.Fprintf(w, "taskforge_task_processing_seconds %f\n", stats.AverageLatency.Seconds())
}

// handleStats handles GET /api/v1/stats.
// Returns system statistics in JSON format.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats := s.metrics.GetStats()

	// Count tasks by status
	pendingCount, _ := s.storage.Count(&storage.ListFilter{Status: ptr(task.StatusPending)})
	runningCount, _ := s.storage.Count(&storage.ListFilter{Status: ptr(task.StatusRunning)})
	completedCount, _ := s.storage.Count(&storage.ListFilter{Status: ptr(task.StatusCompleted)})
	failedCount, _ := s.storage.Count(&storage.ListFilter{Status: ptr(task.StatusFailed)})

	response := map[string]interface{}{
		"queue_size": s.queue.Len(),
		"tasks": map[string]int{
			"pending":   pendingCount,
			"running":   runningCount,
			"completed": completedCount,
			"failed":    failedCount,
		},
		"metrics": map[string]interface{}{
			"tasks_created":   stats.TasksCreated,
			"tasks_processed": stats.TasksProcessed,
			"tasks_failed":    stats.TasksFailed,
			"avg_latency_ms":  stats.AverageLatency.Milliseconds(),
		},
		"http_requests": stats.HTTPRequests,
	}

	s.writeJSON(w, http.StatusOK, response)
}

// writeJSON writes a JSON response with the given status code.
// This helper ensures consistent JSON formatting across all endpoints.
func (s *Server) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	// Use Encoder instead of Marshal for streaming (more efficient for large responses)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ") // Pretty print for readability

	if err := encoder.Encode(data); err != nil {
		s.logger.Printf("Failed to encode JSON response: %v", err)
	}
}

// writeError writes a JSON error response.
// Consistent error format makes API consumption easier.
func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{
		"error": message,
	})
}

// Start begins listening for HTTP requests.
// This method blocks until the server is shut down.
func (s *Server) Start() error {
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
// It waits for in-flight requests to complete.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// ptr is a helper to get a pointer to a value.
// Useful for optional fields in structs.
func ptr[T any](v T) *T {
	return &v
}

// handleStatic serves the web UI.
// In production, you'd use a proper static file server or CDN.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Only serve index.html for root path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Read and serve the HTML file
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		s.logger.Printf("Failed to read index.html: %v", err)
		http.Error(w, "UI not available", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(html)
}
