package main

import (
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// task represents a unit of work submitted to the pool.
type task struct {
	id int
	fn func() error
}

// result carries the outcome of a completed task.
type result struct {
	taskID int
	err    error
}

// WorkerPool manages a fixed-size goroutine pool.
type WorkerPool struct {
	workers    int
	tasks      chan task
	results    chan result
	taskWg     sync.WaitGroup // tracks submitted tasks
	workerWg   sync.WaitGroup // tracks worker goroutines
	counter    int            // unique task ID generator (single-writer: Submit only)
}

// NewWorkerPool creates a pool with the given number of worker goroutines.
func NewWorkerPool(workers int) *WorkerPool {
	return &WorkerPool{
		workers: workers,
		tasks:   make(chan task, workers*2),
		results: make(chan result, workers*2),
	}
}

// Submit enqueues a function for execution and returns its task ID.
func (wp *WorkerPool) Submit(fn func() error) int {
	wp.counter++
	id := wp.counter
	wp.taskWg.Add(1)
	wp.tasks <- task{id: id, fn: fn}
	return id
}

// Start launches the worker goroutines. Each worker reads from the tasks
// channel, executes the function, and sends the result back.
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workers; i++ {
		wp.workerWg.Add(1)
		go func(workerID int) {
			defer wp.workerWg.Done()
			for t := range wp.tasks {
				err := t.fn()
				wp.results <- result{taskID: t.id, err: err}
				wp.taskWg.Done()
			}
		}(i)
	}
}

// Wait blocks until all submitted tasks have completed, then returns every
// error that occurred during execution.
func (wp *WorkerPool) Wait() []error {
	// Drain results in a background goroutine.
	var errs []error
	var mu sync.Mutex
	done := make(chan struct{})

	go func() {
		for r := range wp.results {
			if r.err != nil {
				mu.Lock()
				errs = append(errs, r.err)
				mu.Unlock()
			}
		}
		close(done)
	}()

	// Wait for every submitted task to finish.
	wp.taskWg.Wait()

	// All tasks done — close the tasks channel so workers exit.
	close(wp.tasks)

	// Wait for all workers to exit (they won't send more results).
	wp.workerWg.Wait()

	// Close results — the drain goroutine will finish.
	close(wp.results)

	<-done // wait for drain goroutine to collect all errors
	return errs
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func demo() {
	const numWorkers = 5
	const numTasks = 20

	pool := NewWorkerPool(numWorkers)
	pool.Start()

	fmt.Printf("Worker Pool: %d workers, %d tasks\n\n", numWorkers, numTasks)

	// Submit 20 tasks; some of them will fail on purpose.
	for i := 1; i <= numTasks; i++ {
		taskID := i
		pool.Submit(func() error {
			time.Sleep(time.Duration(50+taskID*10) * time.Millisecond)
			if taskID%7 == 0 {
				return fmt.Errorf("task %d: simulated failure (divisible by 7)", taskID)
			}
			fmt.Printf("  [OK]   task %d completed\n", taskID)
			return nil
		})
	}

	fmt.Println("All tasks submitted, waiting for completion...")
	start := time.Now()
	errs := pool.Wait()
	elapsed := time.Since(start)

	fmt.Printf("\nDone in %v. Errors: %d\n", elapsed, len(errs))
	for _, e := range errs {
		fmt.Printf("  [ERR]  %s\n", e.Error())
	}
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	demo()
}
