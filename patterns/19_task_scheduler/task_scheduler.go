package main

import (
	"container/heap"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Priority queue (min-heap ordered by executeAt, then priority)
// ---------------------------------------------------------------------------

// scheduledTask represents a task scheduled for future execution.
type scheduledTask struct {
	id       int
	executeAt time.Time
	priority int
	fn       func()
	index    int // maintained by container/heap
	cancelled bool
}

// taskHeap is a min-heap of scheduledTask ordered by executeAt then priority.
type taskHeap []*scheduledTask

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	if h[i].executeAt.Equal(h[j].executeAt) {
		return h[i].priority < h[j].priority // lower number = higher priority
	}
	return h[i].executeAt.Before(h[j].executeAt)
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x interface{}) {
	n := len(*h)
	item := x.(*scheduledTask)
	item.index = n
	*h = append(*h, item)
}

func (h *taskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[:n-1]
	return item
}

// ---------------------------------------------------------------------------
// Scheduler
// ---------------------------------------------------------------------------

// Scheduler manages time-based task execution using a priority queue.
type Scheduler struct {
	mu      sync.Mutex
	tasks   *taskHeap
	nextID  int
	done    chan struct{}
	wg      sync.WaitGroup
	running bool
}

// NewScheduler creates a new task scheduler.
func NewScheduler() *Scheduler {
	h := &taskHeap{}
	heap.Init(h)
	return &Scheduler{
		tasks: h,
		done:  make(chan struct{}),
	}
}

// Schedule adds a task to be executed after the given delay with the given
// priority. Returns the task ID which can be used to cancel the task.
func (s *Scheduler) Schedule(fn func(), delay time.Duration, priority int) int {
	return s.ScheduleAt(fn, time.Now().Add(delay), priority)
}

// ScheduleAt adds a task to be executed at the exact given time with the given
// priority. Returns the task ID.
func (s *Scheduler) ScheduleAt(fn func(), executeAt time.Time, priority int) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := s.nextID
	t := &scheduledTask{
		id:        id,
		executeAt: executeAt,
		priority:  priority,
		fn:        fn,
	}
	heap.Push(s.tasks, t)
	return id
}

// Cancel marks a task as cancelled. It will be lazily removed when it reaches
// the top of the heap. Returns true if the task was found and not yet executed.
func (s *Scheduler) Cancel(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range *s.tasks {
		if t.id == id && !t.cancelled {
			t.cancelled = true
			return true
		}
	}
	return false
}

// peekNext returns the next due (non-cancelled) task without removing it.
// Caller must hold s.mu.
func (s *Scheduler) peekNext() *scheduledTask {
	for s.tasks.Len() > 0 {
		t := (*s.tasks)[0]
		if !t.cancelled {
			return t
		}
		// Lazy removal of cancelled tasks.
		heap.Pop(s.tasks)
	}
	return nil
}

// Start begins the scheduler loop in a background goroutine.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			s.mu.Lock()
			next := s.peekNext()
			if next == nil {
				s.mu.Unlock()
				select {
				case <-s.done:
					return
				case <-time.After(50 * time.Millisecond):
					continue
				}
			}

			waitDuration := time.Until(next.executeAt)
			if waitDuration > 0 {
				// Sleep until the next task is due, but wake on Stop.
				s.mu.Unlock()
				select {
				case <-s.done:
					return
				case <-time.After(waitDuration):
				}
			} else {
				s.mu.Unlock()
			}

			// Re-lock and execute all due tasks.
			s.mu.Lock()
			for {
				t := s.peekNext()
				if t == nil || time.Now().Before(t.executeAt) {
					break
				}
				heap.Pop(s.tasks)
				s.mu.Unlock()
				t.fn()
				s.mu.Lock()
			}
			s.mu.Unlock()
		}
	}()
}

// Stop signals the scheduler to stop and waits for the background goroutine
// to exit. Already-running task functions will complete before Stop returns.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.done)
	s.mu.Unlock()
	s.wg.Wait()
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func demo() {
	s := NewScheduler()
	s.Start()

	fmt.Println("=== Task Scheduler Demo ===")
	fmt.Println()

	// Use explicit timestamps so tasks with the same delay share the same
	// executeAt, making the priority ordering deterministic.
	now := time.Now()

	fmt.Println("Scheduling 6 tasks with varying delays and priorities...")
	id1 := s.ScheduleAt(func() {
		fmt.Printf("  [exec] Task %d: priority=1, delay=300ms (low priority, medium delay)\n", 1)
	}, now.Add(300*time.Millisecond), 1)

	id2 := s.ScheduleAt(func() {
		fmt.Printf("  [exec] Task %d: priority=0, delay=300ms (high priority, medium delay)\n", 2)
	}, now.Add(300*time.Millisecond), 0)

	s.ScheduleAt(func() {
		fmt.Printf("  [exec] Task %d: priority=5, delay=100ms (default priority, short delay)\n", 3)
	}, now.Add(100*time.Millisecond), 5)

	id4 := s.ScheduleAt(func() {
		fmt.Printf("  [exec] Task %d: priority=2, delay=200ms (medium priority, short delay)\n", 4)
	}, now.Add(200*time.Millisecond), 2)

	s.ScheduleAt(func() {
		fmt.Printf("  [exec] Task %d: priority=3, delay=400ms (medium priority, long delay)\n", 5)
	}, now.Add(400*time.Millisecond), 3)

	id6 := s.ScheduleAt(func() {
		fmt.Printf("  [exec] Task %d: priority=0, delay=200ms (high priority, short delay)\n", 6)
	}, now.Add(200*time.Millisecond), 0)

	fmt.Printf("  Scheduled tasks: 1(pri=1,300ms), 2(pri=0,300ms), 3(pri=5,100ms), 4(pri=2,200ms), 5(pri=3,400ms), 6(pri=0,200ms)\n")

	// Cancel task 4 before it executes.
	fmt.Printf("\n  Cancelling task %d...\n", id4)
	cancelled := s.Cancel(id4)
	fmt.Printf("  Cancel task %d: %v\n", id4, cancelled)

	// Try cancelling task 3 which may have already executed.
	time.Sleep(120 * time.Millisecond)
	cancelled3 := s.Cancel(3)
	fmt.Printf("  Cancel task 3 (likely already executed): %v\n", cancelled3)

	// Wait for all tasks to complete.
	fmt.Println()
	time.Sleep(500 * time.Millisecond)

	// Try cancelling already-executed tasks.
	cancelled1 := s.Cancel(id1)
	cancelled2 := s.Cancel(id2)
	cancelled6 := s.Cancel(id6)
	fmt.Printf("  Cancel task %d (already executed): %v\n", id1, cancelled1)
	fmt.Printf("  Cancel task %d (already executed): %v\n", id2, cancelled2)
	fmt.Printf("  Cancel task %d (already executed): %v\n", id6, cancelled6)

	s.Stop()
	fmt.Println("\nScheduler stopped.")
	fmt.Println()
	fmt.Println("Expected execution order: 3 (100ms) -> 6 (200ms, pri=0) -> 2 (300ms, pri=0) -> 1 (300ms, pri=1) -> 5 (400ms)")
	fmt.Println("Task 4 was cancelled and should not appear.")
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	demo()
}
