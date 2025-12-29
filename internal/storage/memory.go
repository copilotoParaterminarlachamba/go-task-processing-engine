// Package storage provides persistence mechanisms for tasks.
// This package defines the Storage interface and provides implementations.
// Currently implements in-memory storage; can be extended to support
// Redis, PostgreSQL, MongoDB, etc.
//
// The interface segregation principle (from SOLID) is applied here:
// the interface only contains methods needed by consumers.
package storage

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/smart-developer1791/go-task-processing-engine/internal/task"
)

// Common errors returned by storage operations.
var (
	// ErrNotFound indicates the requested task doesn't exist.
	ErrNotFound = errors.New("task not found")

	// ErrAlreadyExists indicates a task with the same ID already exists.
	ErrAlreadyExists = errors.New("task already exists")
)

// Storage defines the interface for task persistence.
// Using an interface allows easy substitution of storage backends
// for testing (mocks) or different deployment scenarios.
type Storage interface {
	// Create stores a new task.
	// Returns ErrAlreadyExists if a task with the same ID exists.
	Create(t *task.Task) error

	// Get retrieves a task by ID.
	// Returns ErrNotFound if the task doesn't exist.
	Get(id string) (*task.Task, error)

	// Update modifies an existing task.
	// Returns ErrNotFound if the task doesn't exist.
	Update(t *task.Task) error

	// Delete removes a task.
	// Returns ErrNotFound if the task doesn't exist.
	Delete(id string) error

	// List returns all tasks matching the optional filter.
	// If filter is nil, returns all tasks.
	List(filter *ListFilter) ([]*task.Task, error)

	// Count returns the number of tasks matching the filter.
	Count(filter *ListFilter) (int, error)
}

// ListFilter specifies criteria for listing tasks.
// All fields are optional - nil/zero values mean "don't filter".
type ListFilter struct {
	// Status filters by task status.
	Status *task.Status

	// Type filters by task type.
	Type *string

	// Priority filters by exact priority.
	Priority *task.Priority

	// MinPriority filters for priority >= this value.
	MinPriority *task.Priority

	// CreatedAfter filters for tasks created after this time.
	CreatedAfter *time.Time

	// CreatedBefore filters for tasks created before this time.
	CreatedBefore *time.Time

	// Limit restricts the number of results.
	// Zero means no limit.
	Limit int

	// Offset skips the first N results (for pagination).
	Offset int
}

// MemoryStorage is an in-memory implementation of Storage.
// It uses a map for O(1) lookups and a mutex for thread safety.
//
// Limitations:
// - Data is lost on restart (not persistent)
// - Memory usage grows with task count (no automatic cleanup)
// - Not suitable for distributed deployments (not shared)
//
// Use cases:
// - Development and testing
// - Single-instance deployments with acceptable data loss
// - Caching layer in front of persistent storage
type MemoryStorage struct {
	// mu protects concurrent access to the tasks map.
	// RWMutex allows multiple concurrent readers, improving
	// performance for read-heavy workloads.
	mu sync.RWMutex

	// tasks maps task ID to task data.
	// Using a map provides O(1) average-case lookups.
	tasks map[string]*task.Task
}

// NewMemoryStorage creates a new in-memory storage instance.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		tasks: make(map[string]*task.Task),
	}
}

// Create stores a new task.
// Returns ErrAlreadyExists if a task with the same ID already exists.
func (s *MemoryStorage) Create(t *task.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[t.ID]; exists {
		return ErrAlreadyExists
	}

	// Store a copy to prevent external modification.
	// This is a defensive copy pattern - the caller can't
	// accidentally corrupt our internal state by modifying
	// the task after Create returns.
	s.tasks[t.ID] = copyTask(t)
	return nil
}

// Get retrieves a task by ID.
// Returns ErrNotFound if the task doesn't exist.
func (s *MemoryStorage) Get(id string) (*task.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, exists := s.tasks[id]
	if !exists {
		return nil, ErrNotFound
	}

	// Return a copy to prevent external modification.
	return copyTask(t), nil
}

// Update modifies an existing task.
// The entire task is replaced (not a partial update).
func (s *MemoryStorage) Update(t *task.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[t.ID]; !exists {
		return ErrNotFound
	}

	s.tasks[t.ID] = copyTask(t)
	return nil
}

// Delete removes a task.
// Returns ErrNotFound if the task doesn't exist.
func (s *MemoryStorage) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[id]; !exists {
		return ErrNotFound
	}

	delete(s.tasks, id)
	return nil
}

// List returns all tasks matching the filter criteria.
// Results are returned in no particular order (map iteration order).
// For consistent ordering, the caller should sort the results.
func (s *MemoryStorage) List(filter *ListFilter) ([]*task.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Pre-allocate with estimated size for efficiency.
	// Using len(s.tasks) as estimate; actual result may be smaller.
	result := make([]*task.Task, 0, len(s.tasks))

	skipped := 0
	for _, t := range s.tasks {
		// Apply filters
		if !matchesFilter(t, filter) {
			continue
		}

		// Handle offset (pagination)
		if filter != nil && filter.Offset > skipped {
			skipped++
			continue
		}

		// Handle limit (pagination)
		if filter != nil && filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}

		result = append(result, copyTask(t))
	}

	return result, nil
}

// Count returns the number of tasks matching the filter.
// More efficient than List when only the count is needed.
func (s *MemoryStorage) Count(filter *ListFilter) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, t := range s.tasks {
		if matchesFilter(t, filter) {
			count++
		}
	}

	return count, nil
}

// matchesFilter checks if a task matches the filter criteria.
// Returns true if filter is nil (no filtering).
func matchesFilter(t *task.Task, filter *ListFilter) bool {
	if filter == nil {
		return true
	}

	// Check status filter
	if filter.Status != nil && t.Status != *filter.Status {
		return false
	}

	// Check type filter
	if filter.Type != nil && t.Type != *filter.Type {
		return false
	}

	// Check exact priority filter
	if filter.Priority != nil && t.Priority != *filter.Priority {
		return false
	}

	// Check minimum priority filter
	if filter.MinPriority != nil && t.Priority < *filter.MinPriority {
		return false
	}

	// Check time range filters
	if filter.CreatedAfter != nil && t.CreatedAt.Before(*filter.CreatedAfter) {
		return false
	}
	if filter.CreatedBefore != nil && t.CreatedAt.After(*filter.CreatedBefore) {
		return false
	}

	return true
}

// copyTask creates a deep copy of a task.
// This prevents modifications to the copy from affecting the original.
// Deep copy is important for slices and maps within the struct.
func copyTask(t *task.Task) *task.Task {
	if t == nil {
		return nil
	}

	// Create a shallow copy first
	copy := *t

	// Deep copy pointer fields
	if t.StartedAt != nil {
		startedAt := *t.StartedAt
		copy.StartedAt = &startedAt
	}
	if t.CompletedAt != nil {
		completedAt := *t.CompletedAt
		copy.CompletedAt = &completedAt
	}

	// Deep copy slices (json.RawMessage is []byte)
	if t.Payload != nil {
		copy.Payload = make(json.RawMessage, len(t.Payload))
		copyBytes(copy.Payload, t.Payload)
	}
	if t.Result != nil {
		copy.Result = make(json.RawMessage, len(t.Result))
		copyBytes(copy.Result, t.Result)
	}

	return &copy
}

// copyBytes is a helper to copy byte slices.
// Using copy() built-in for efficiency.
func copyBytes(dst, src []byte) {
	copy(dst, src)
}
