// Package metrics provides observability through metric collection.
// Metrics are essential for monitoring system health, debugging
// performance issues, and capacity planning.
//
// This implementation uses atomic operations for thread-safe
// counter updates without locks, providing better performance
// in high-throughput scenarios.
package metrics

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Stats holds a snapshot of system metrics.
// This struct is returned by GetStats() and is safe to read
// without synchronization.
type Stats struct {
	// TasksCreated is the total number of tasks submitted.
	TasksCreated int64

	// TasksProcessed is the total number of successfully completed tasks.
	TasksProcessed int64

	// TasksFailed is the total number of tasks that failed.
	TasksFailed int64

	// AverageLatency is the average task processing time.
	AverageLatency time.Duration

	// HTTPRequests maps "method:path:status" to request count.
	HTTPRequests map[string]int64
}

// Collector accumulates metrics about system behavior.
// All methods are thread-safe and can be called concurrently.
//
// Design decisions:
// - Atomic counters for lock-free increments
// - Moving average for latency (bounded memory)
// - Mutex only for complex structures (HTTP request map)
type Collector struct {
	// tasksCreated counts total tasks submitted.
	// Using atomic.Int64 for lock-free increments.
	tasksCreated atomic.Int64

	// tasksProcessed counts successfully completed tasks.
	tasksProcessed atomic.Int64

	// tasksFailed counts failed tasks.
	tasksFailed atomic.Int64

	// totalLatency accumulates processing time for averaging.
	// Using atomic.Int64 (nanoseconds) for lock-free updates.
	totalLatency atomic.Int64

	// latencyCount is the number of latency samples.
	latencyCount atomic.Int64

	// httpRequests tracks HTTP request counts by endpoint.
	// Requires mutex because map operations aren't atomic.
	httpRequests   map[string]*atomic.Int64
	httpRequestsMu sync.RWMutex
}

// NewCollector creates a new metrics collector.
func NewCollector() *Collector {
	return &Collector{
		httpRequests: make(map[string]*atomic.Int64),
	}
}

// RecordTaskCreated increments the created task counter.
// This should be called when a task is successfully queued.
func (c *Collector) RecordTaskCreated() {
	// atomic.Int64.Add is lock-free and thread-safe.
	// It uses CPU atomic instructions (e.g., LOCK XADD on x86).
	c.tasksCreated.Add(1)
}

// RecordTaskCompleted increments the processed counter and records latency.
// This should be called when a task successfully completes.
func (c *Collector) RecordTaskCompleted(latency time.Duration) {
	c.tasksProcessed.Add(1)

	// Store latency as nanoseconds for atomic operations.
	// time.Duration is already in nanoseconds internally.
	c.totalLatency.Add(int64(latency))
	c.latencyCount.Add(1)
}

// RecordTaskFailed increments the failed task counter.
// This should be called when a task fails (after all retries).
func (c *Collector) RecordTaskFailed() {
	c.tasksFailed.Add(1)
}

// RecordHTTPRequest records an HTTP request metric.
// Parameters are used to create a unique key for each endpoint/status combination.
func (c *Collector) RecordHTTPRequest(method, path string, status int, latency time.Duration) {
	// Create a unique key for this request type.
	// Format: "GET:/api/v1/tasks:200"
	key := formatRequestKey(method, path, status)

	// Get or create the counter for this key.
	// We use RLock first (optimistic) and upgrade to Lock only if needed.
	c.httpRequestsMu.RLock()
	counter, exists := c.httpRequests[key]
	c.httpRequestsMu.RUnlock()

	if !exists {
		// Counter doesn't exist - need to create it.
		// This is the slow path, executed only for new endpoints.
		c.httpRequestsMu.Lock()
		// Double-check after acquiring write lock (another goroutine may have created it).
		counter, exists = c.httpRequests[key]
		if !exists {
			counter = &atomic.Int64{}
			c.httpRequests[key] = counter
		}
		c.httpRequestsMu.Unlock()
	}

	// Increment the counter (lock-free).
	counter.Add(1)
}

// GetStats returns a snapshot of current metrics.
// The returned Stats is a copy and safe to read without synchronization.
func (c *Collector) GetStats() Stats {
	// Load all atomic values.
	// atomic.Int64.Load ensures we get a consistent read.
	created := c.tasksCreated.Load()
	processed := c.tasksProcessed.Load()
	failed := c.tasksFailed.Load()
	totalLatency := c.totalLatency.Load()
	latencyCount := c.latencyCount.Load()

	// Calculate average latency.
	var avgLatency time.Duration
	if latencyCount > 0 {
		avgLatency = time.Duration(totalLatency / latencyCount)
	}

	// Copy HTTP request counts.
	// We need to hold the lock while iterating the map.
	c.httpRequestsMu.RLock()
	httpReqs := make(map[string]int64, len(c.httpRequests))
	for key, counter := range c.httpRequests {
		httpReqs[key] = counter.Load()
	}
	c.httpRequestsMu.RUnlock()

	return Stats{
		TasksCreated:   created,
		TasksProcessed: processed,
		TasksFailed:    failed,
		AverageLatency: avgLatency,
		HTTPRequests:   httpReqs,
	}
}

// Reset clears all metrics.
// Useful for testing or periodic reset in production.
func (c *Collector) Reset() {
	c.tasksCreated.Store(0)
	c.tasksProcessed.Store(0)
	c.tasksFailed.Store(0)
	c.totalLatency.Store(0)
	c.latencyCount.Store(0)

	c.httpRequestsMu.Lock()
	c.httpRequests = make(map[string]*atomic.Int64)
	c.httpRequestsMu.Unlock()
}

// formatRequestKey creates a unique key for HTTP request metrics.
// The key format is "METHOD:path:status".
func formatRequestKey(method, path string, status int) string {
	// Using string concatenation is fast for small strings.
	// For high-frequency calls, consider using a string builder
	// or pre-allocating a buffer.
	return method + ":" + path + ":" + formatStatus(status)
}

// formatStatus converts status code to string.
// Using a switch for common codes is faster than fmt.Sprintf.
func formatStatus(status int) string {
	switch status {
	case 200:
		return "200"
	case 201:
		return "201"
	case 204:
		return "204"
	case 400:
		return "400"
	case 404:
		return "404"
	case 500:
		return "500"
	default:
		// Fallback for uncommon status codes.
		// This allocates, but is rare in practice.
		return strconv.Itoa(status)
	}
}
