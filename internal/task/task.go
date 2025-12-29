// Package task defines the core Task entity and related types.
// This package follows the "domain-driven design" approach where
// the Task type represents a unit of work in our domain.
package task

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Priority represents the urgency level of a task.
// Higher values indicate higher priority (processed first).
// Using a custom type (instead of raw int) provides type safety
// and makes the code more self-documenting.
type Priority int

// Predefined priority levels following the "iota" pattern.
// This is idiomatic Go for creating enumerated constants.
const (
	PriorityLow    Priority = iota // 0 - Background tasks, batch jobs
	PriorityNormal                 // 1 - Regular user requests
	PriorityHigh                   // 2 - Important operations
	PriorityCritical               // 3 - System-critical tasks
)

// String implements the Stringer interface for human-readable output.
// This is useful for logging, debugging, and API responses.
func (p Priority) String() string {
	// Using a map instead of switch for cleaner code.
	// The trade-off is slightly more memory for the map.
	names := map[Priority]string{
		PriorityLow:      "low",
		PriorityNormal:   "normal",
		PriorityHigh:     "high",
		PriorityCritical: "critical",
	}
	if name, ok := names[p]; ok {
		return name
	}
	return "unknown"
}

// Status represents the current state of a task in its lifecycle.
// Tasks follow this state machine:
// Pending -> Running -> Completed/Failed
//                   \-> Cancelled
type Status string

const (
	StatusPending   Status = "pending"   // Waiting in queue
	StatusRunning   Status = "running"   // Being processed by a worker
	StatusCompleted Status = "completed" // Successfully finished
	StatusFailed    Status = "failed"    // Finished with error
	StatusCancelled Status = "cancelled" // Cancelled before completion
)

// Task represents a unit of work to be processed.
// The struct uses pointer fields for optional data and value fields
// for required data. This is a common Go pattern for API types.
type Task struct {
	// ID is a unique identifier using UUID v4.
	// UUIDs are ideal for distributed systems as they can be
	// generated without coordination between nodes.
	ID string `json:"id"`

	// Type categorizes the task for routing to appropriate handlers.
	// Examples: "email.send", "image.resize", "report.generate"
	Type string `json:"type"`

	// Payload contains task-specific data as a JSON object.
	// Using json.RawMessage allows us to delay parsing until
	// the specific handler processes the task.
	Payload json.RawMessage `json:"payload"`

	// Priority determines processing order.
	// Higher priority tasks are dequeued before lower priority ones.
	Priority Priority `json:"priority"`

	// Status tracks the current state in the task lifecycle.
	Status Status `json:"status"`

	// CreatedAt records when the task was submitted.
	// All timestamps use UTC to avoid timezone confusion.
	CreatedAt time.Time `json:"created_at"`

	// StartedAt records when a worker began processing.
	// Nil if the task hasn't started yet.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// CompletedAt records when processing finished.
	// Nil if the task is still pending or running.
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Result stores the output of successful task execution.
	// The format depends on the task type.
	Result json.RawMessage `json:"result,omitempty"`

	// Error contains the error message if the task failed.
	// Only populated when Status == StatusFailed.
	Error string `json:"error,omitempty"`

	// RetryCount tracks how many times this task has been retried.
	// Used to implement exponential backoff and max retry limits.
	RetryCount int `json:"retry_count"`

	// MaxRetries is the maximum number of retry attempts.
	// Set to 0 for no retries, -1 for unlimited retries.
	MaxRetries int `json:"max_retries"`
}

// Validation errors using sentinel errors pattern.
// Sentinel errors allow callers to check for specific error types
// using errors.Is(), which is preferred over string comparison.
var (
	ErrEmptyType       = errors.New("task type cannot be empty")
	ErrInvalidPriority = errors.New("priority must be between 0 and 3")
	ErrNilPayload      = errors.New("payload cannot be nil")
)

// CreateRequest represents the API request body for creating a task.
// Separating request/response types from domain types is a best practice
// that allows API evolution without affecting internal logic.
type CreateRequest struct {
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Priority   Priority        `json:"priority"`
	MaxRetries int             `json:"max_retries"`
}

// Validate checks if the CreateRequest contains valid data.
// Validation at the boundary (API layer) prevents invalid data
// from entering the system, following the "fail fast" principle.
func (r *CreateRequest) Validate() error {
	if r.Type == "" {
		return ErrEmptyType
	}
	if r.Priority < PriorityLow || r.Priority > PriorityCritical {
		return ErrInvalidPriority
	}
	if r.Payload == nil {
		return ErrNilPayload
	}
	return nil
}

// New creates a new Task from a CreateRequest.
// This is a factory function pattern, which is preferred over
// direct struct initialization when initialization logic is needed.
func New(req *CreateRequest) *Task {
	now := time.Now().UTC()
	return &Task{
		// Generate a new UUID for the task.
		// UUID v4 provides 122 bits of randomness, making collisions
		// practically impossible (would need ~2^61 UUIDs for 50% chance).
		ID:         uuid.New().String(),
		Type:       req.Type,
		Payload:    req.Payload,
		Priority:   req.Priority,
		Status:     StatusPending,
		CreatedAt:  now,
		MaxRetries: req.MaxRetries,
	}
}

// MarkStarted transitions the task to the running state.
// Methods that modify state are named with action verbs (Mark, Set, Update)
// to make the intent clear.
func (t *Task) MarkStarted() {
	now := time.Now().UTC()
	t.StartedAt = &now
	t.Status = StatusRunning
}

// MarkCompleted transitions the task to the completed state.
// The result parameter contains the serialized output from the handler.
func (t *Task) MarkCompleted(result json.RawMessage) {
	now := time.Now().UTC()
	t.CompletedAt = &now
	t.Status = StatusCompleted
	t.Result = result
}

// MarkFailed transitions the task to the failed state.
// The error message is stored for debugging and observability.
func (t *Task) MarkFailed(err error) {
	now := time.Now().UTC()
	t.CompletedAt = &now
	t.Status = StatusFailed
	if err != nil {
		t.Error = err.Error()
	}
}

// CanRetry checks if the task can be retried after a failure.
// Returns true if retry count hasn't exceeded the maximum,
// or if MaxRetries is -1 (unlimited).
func (t *Task) CanRetry() bool {
	if t.MaxRetries == -1 {
		return true // Unlimited retries
	}
	return t.RetryCount < t.MaxRetries
}

// IncrementRetry increases the retry counter and resets the task
// to pending status for reprocessing.
func (t *Task) IncrementRetry() {
	t.RetryCount++
	t.Status = StatusPending
	t.StartedAt = nil
	t.CompletedAt = nil
	t.Error = ""
}

// Duration calculates how long the task took to process.
// Returns 0 if the task hasn't completed or hasn't started.
func (t *Task) Duration() time.Duration {
	if t.StartedAt == nil || t.CompletedAt == nil {
		return 0
	}
	return t.CompletedAt.Sub(*t.StartedAt)
}

// WaitTime calculates how long the task waited in the queue.
// This metric is useful for monitoring queue health and
// identifying processing bottlenecks.
func (t *Task) WaitTime() time.Duration {
	if t.StartedAt == nil {
		// Task hasn't started, calculate wait time until now
		return time.Since(t.CreatedAt)
	}
	return t.StartedAt.Sub(t.CreatedAt)
}
