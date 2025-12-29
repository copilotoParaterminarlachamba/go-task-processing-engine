// Package queue implements a thread-safe priority queue for tasks.
// This implementation uses Go's container/heap package which provides
// heap operations on any type that implements heap.Interface.
//
// Priority queues are essential for task scheduling systems where
// some tasks need to be processed before others regardless of
// their arrival time.
package queue

import (
	"container/heap"
	"context"
	"errors"
	"sync"

	"github.com/smart-developer1791/go-task-processing-engine/internal/task"
)

// Queue errors using sentinel error pattern for type-safe error handling.
var (
	// ErrQueueFull is returned when trying to push to a full queue.
	// Callers can handle this by implementing backpressure strategies:
	// - Blocking wait until space is available
	// - Rejecting the request with HTTP 503
	// - Dropping low-priority tasks to make room
	ErrQueueFull = errors.New("queue is at capacity")

	// ErrQueueClosed is returned when operating on a closed queue.
	ErrQueueClosed = errors.New("queue is closed")
)

// taskHeap is an internal type that implements heap.Interface.
// We use a separate type to keep the heap implementation details
// hidden from the public PriorityQueue API.
type taskHeap []*task.Task

// Len returns the number of elements in the heap.
// Required by sort.Interface (embedded in heap.Interface).
func (h taskHeap) Len() int {
	return len(h)
}

// Less defines the ordering of elements.
// For a max-heap (highest priority first), we return true
// when element i has HIGHER priority than element j.
//
// Tie-breaking by CreatedAt ensures FIFO order within the same priority,
// preventing starvation of older tasks.
func (h taskHeap) Less(i, j int) bool {
	// Higher priority value = should be processed first
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority
	}
	// Same priority: earlier creation time = process first (FIFO)
	return h[i].CreatedAt.Before(h[j].CreatedAt)
}

// Swap exchanges two elements in the heap.
// Required by sort.Interface (embedded in heap.Interface).
func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

// Push adds an element to the heap.
// Required by heap.Interface.
// Note: This is called by heap.Push(), not directly.
// The element comes as interface{} and must be type-asserted.
func (h *taskHeap) Push(x interface{}) {
	// Type assertion with ok check is safer, but here we trust
	// that only tasks are pushed (controlled by PriorityQueue).
	*h = append(*h, x.(*task.Task))
}

// Pop removes and returns the last element from the heap.
// Required by heap.Interface.
// Note: heap.Pop() moves the min/max element to the end first,
// then calls this method to remove it.
func (h *taskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // Avoid memory leak by clearing the pointer
	*h = old[0 : n-1]
	return item
}

// PriorityQueue is a thread-safe priority queue for tasks.
// It wraps taskHeap with synchronization and adds capacity control.
//
// Thread safety is achieved using sync.Mutex for mutual exclusion
// and sync.Cond for efficient waiting when the queue is empty.
type PriorityQueue struct {
	// mu protects all fields below.
	// Using a pointer to sync.Mutex allows the struct to be copied
	// if needed (though copying sync primitives is generally discouraged).
	mu *sync.Mutex

	// cond is used to signal waiting consumers when new items arrive.
	// This is more efficient than busy-waiting or polling.
	cond *sync.Cond

	// heap is the underlying priority heap.
	heap taskHeap

	// capacity is the maximum number of items the queue can hold.
	// Zero means unlimited capacity.
	capacity int

	// closed indicates whether the queue has been shut down.
	// Once closed, no new items can be pushed.
	closed bool
}

// NewPriorityQueue creates a new priority queue with the specified capacity.
// Capacity of 0 means unlimited (bounded only by available memory).
func NewPriorityQueue(capacity int) *PriorityQueue {
	mu := &sync.Mutex{}
	pq := &PriorityQueue{
		mu:       mu,
		cond:     sync.NewCond(mu),
		heap:     make(taskHeap, 0, capacity),
		capacity: capacity,
	}
	// Initialize the heap.
	// While not strictly necessary for an empty heap,
	// it's good practice for consistency.
	heap.Init(&pq.heap)
	return pq
}

// Push adds a task to the queue.
// Returns ErrQueueFull if the queue is at capacity.
// Returns ErrQueueClosed if the queue has been closed.
//
// Time complexity: O(log n) where n is the number of items in the queue.
func (pq *PriorityQueue) Push(t *task.Task) error {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// Check if queue is closed
	if pq.closed {
		return ErrQueueClosed
	}

	// Check capacity (if set)
	if pq.capacity > 0 && pq.heap.Len() >= pq.capacity {
		return ErrQueueFull
	}

	// Add to heap
	heap.Push(&pq.heap, t)

	// Signal one waiting consumer that an item is available.
	// Using Signal() instead of Broadcast() is more efficient
	// when there are multiple consumers - only one can get
	// this item anyway.
	pq.cond.Signal()

	return nil
}

// Pop removes and returns the highest priority task.
// Blocks if the queue is empty until an item is available
// or the context is cancelled.
//
// This method implements the "blocking queue" pattern commonly
// used in producer-consumer scenarios.
func (pq *PriorityQueue) Pop(ctx context.Context) (*task.Task, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// Wait loop: handle spurious wakeups and check context.
	// Condition variables can wake up spuriously (without Signal/Broadcast),
	// so we always recheck the condition in a loop.
	for pq.heap.Len() == 0 && !pq.closed {
		// Check if context is done before waiting.
		// This enables timeout and cancellation support.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Create a channel to signal context cancellation to the waiting goroutine.
		// This is a pattern to combine sync.Cond with context cancellation,
		// since sync.Cond doesn't natively support context.
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				// Context cancelled - wake up the waiting goroutine
				pq.cond.Broadcast()
			case <-done:
				// Normal wakeup - goroutine will exit
			}
		}()

		// Wait releases the lock and suspends the goroutine until signaled.
		// When Wait returns, the lock is held again.
		pq.cond.Wait()
		close(done)

		// Recheck context after waking up
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	// Check if we woke up due to close with empty queue
	if pq.heap.Len() == 0 {
		return nil, ErrQueueClosed
	}

	// Pop the highest priority item
	item := heap.Pop(&pq.heap).(*task.Task)
	return item, nil
}

// TryPop attempts to pop a task without blocking.
// Returns (nil, nil) if the queue is empty.
// This is useful for non-blocking polling scenarios.
func (pq *PriorityQueue) TryPop() (*task.Task, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if pq.closed {
		return nil, ErrQueueClosed
	}

	if pq.heap.Len() == 0 {
		return nil, nil
	}

	item := heap.Pop(&pq.heap).(*task.Task)
	return item, nil
}

// Len returns the current number of items in the queue.
// This is a snapshot and may change immediately after returning.
func (pq *PriorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.heap.Len()
}

// Close marks the queue as closed.
// No new items can be pushed after Close is called.
// Waiting Pop calls will return ErrQueueClosed once the queue is empty.
func (pq *PriorityQueue) Close() {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	pq.closed = true
	// Wake up all waiting consumers so they can see the queue is closed
	pq.cond.Broadcast()
}

// IsClosed returns whether the queue has been closed.
func (pq *PriorityQueue) IsClosed() bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.closed
}
