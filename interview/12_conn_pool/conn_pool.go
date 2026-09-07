package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// conn wraps a connection object with its creation timestamp for idle check.
type conn struct {
	resource  interface{}
	createdAt time.Time
}

// ConnPool manages a pool of reusable connections with idle timeout and
// maximum open connection limits.
type ConnPool struct {
	idle        chan *conn        // idle connections ready for reuse
	mu          sync.Mutex        // protects openCount
	maxOpen     int               // max simultaneous open connections
	openCount   int               // currently open connections
	idleTimeout time.Duration     // max time a conn can sit idle before eviction
	factory     func() (interface{}, error)
}

// NewConnPool creates a connection pool.
//   - factory: creates new connection objects
//   - maxOpen: maximum simultaneous open connections (0 = unlimited)
//   - idleTimeout: connections idle longer than this are discarded on Get
func NewConnPool(factory func() (interface{}, error), maxOpen int,
	idleTimeout time.Duration) *ConnPool {

	return &ConnPool{
		idle:        make(chan *conn, maxOpen),
		maxOpen:     maxOpen,
		idleTimeout: idleTimeout,
		factory:     factory,
	}
}

// Get retrieves a connection from the pool or creates a new one.
// Returns an error if maxOpen is reached or the factory fails.
func (p *ConnPool) Get() (interface{}, error) {
	// Try to grab an idle connection (non-blocking).
	select {
	case c := <-p.idle:
		if time.Since(c.createdAt) > p.idleTimeout {
			p.mu.Lock()
			p.openCount--
			p.mu.Unlock()
			return p.createNew()
		}
		return c.resource, nil
	default:
	}

	// No idle conn available — check if we can create one.
	p.mu.Lock()
	if p.maxOpen > 0 && p.openCount >= p.maxOpen {
		p.mu.Unlock()
		return nil, errors.New("conn pool: max open connections reached")
	}
	p.openCount++
	p.mu.Unlock()

	return p.createNew()
}

// createNew invokes the factory.
func (p *ConnPool) createNew() (interface{}, error) {
	resource, err := p.factory()
	if err != nil {
		p.mu.Lock()
		p.openCount--
		p.mu.Unlock()
		return nil, fmt.Errorf("conn pool: factory error: %w", err)
	}
	return resource, nil
}

// Put returns a connection to the pool. If the pool is full the connection
// is discarded and openCount is decremented.
func (p *ConnPool) Put(c interface{}) error {
	wrapped := &conn{resource: c, createdAt: time.Now()}

	select {
	case p.idle <- wrapped:
		return nil
	default:
		// Pool full — discard the connection.
		p.mu.Lock()
		p.openCount--
		p.mu.Unlock()
		return nil
	}
}

// Close closes the pool, discarding all idle connections.
func (p *ConnPool) Close() {
	close(p.idle)
	for c := range p.idle {
		_ = c // connections are type-erased; caller should manage cleanup
	}
}

// Len returns the number of idle connections in the pool.
func (p *ConnPool) Len() int {
	return len(p.idle)
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func demo() {
	const maxOpen = 5
	const idleTimeout = 3 * time.Second

	var idCounter int
	factory := func() (interface{}, error) {
		idCounter++
		return fmt.Sprintf("conn-%d", idCounter), nil
	}

	pool := NewConnPool(factory, maxOpen, idleTimeout)

	fmt.Printf("ConnPool: maxOpen=%d, idleTimeout=%v\n\n", maxOpen, idleTimeout)

	// Phase 1: Acquire 3 connections from different goroutines.
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			c, err := pool.Get()
			if err != nil {
				fmt.Printf("  goroutine %d: Get error: %v\n", gid, err)
				return
			}
			fmt.Printf("  goroutine %d: acquired %s (idle=%d, open=%d)\n",
				gid, c, pool.Len(), pool.openCount)
			time.Sleep(200 * time.Millisecond)
			pool.Put(c)
			fmt.Printf("  goroutine %d: returned %s (idle=%d)\n",
				gid, c, pool.Len())
		}(i)
	}
	wg.Wait()

	fmt.Printf("\nAfter phase 1: idle=%d, open=%d\n\n", pool.Len(), pool.openCount)

	// Phase 2: Exhaust pool then try to exceed maxOpen.
	conns := make([]interface{}, 0, maxOpen)
	for i := 0; i < maxOpen; i++ {
		c, err := pool.Get()
		if err != nil {
			fmt.Printf("  unexpected error at acquire %d: %v\n", i, err)
			break
		}
		fmt.Printf("  acquired %s (open=%d)\n", c, pool.openCount)
		conns = append(conns, c)
	}

	// This should fail — pool is exhausted.
	_, err := pool.Get()
	fmt.Printf("  extra Get (should fail): %v\n\n", err)

	// Return them all.
	for _, c := range conns {
		pool.Put(c)
	}
	fmt.Printf("After returning all: idle=%d, open=%d\n\n", pool.Len(), pool.openCount)

	// Phase 3: Idle timeout test.
	c, _ := pool.Get()
	fmt.Printf("Before idle timeout: idle=%d\n", pool.Len())
	pool.Put(c)
	fmt.Printf("  Put back, waiting %v for timeout...\n", idleTimeout+500*time.Millisecond)
	time.Sleep(idleTimeout + 500*time.Millisecond)
	fmt.Printf("After idle timeout: idle=%d\n", pool.Len())

	pool.Close()
	fmt.Println("\nPool closed.")
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	demo()
}
