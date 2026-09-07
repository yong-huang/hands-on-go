package main

import (
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// Producer-Consumer with buffered channel
// ---------------------------------------------------------------------------

// Producer generates numbered items and sends them to the queue channel.
// It signals done via the WaitGroup when all its items are produced.
func producer(id int, count int, queue chan<- int, wg *sync.WaitGroup) {
	defer wg.Done()
	for i := 0; i < count; i++ {
		item := id*1000 + i // unique item number
		queue <- item
		fmt.Printf("  Producer %d: produced item %d\n", id, item)
	}
	fmt.Printf("  Producer %d: finished (%d items)\n", id, count)
}

// Consumer reads items from the queue channel until it is closed,
// then signals done via the WaitGroup.
func consumer(id int, queue <-chan int, consumed *int, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	for item := range queue {
		fmt.Printf("  Consumer %d: consumed item %d\n", id, item)
		mu.Lock()
		*consumed++
		mu.Unlock()
	}
	fmt.Printf("  Consumer %d: finished\n", id)
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func demo() {
	const numProducers = 3
	const numConsumers = 2
	const bufferSize = 10
	const totalItems = 50

	base := totalItems / numProducers      // 16
	remainder := totalItems % numProducers  // 2

	queue := make(chan int, bufferSize)

	var prodWg sync.WaitGroup // tracks producer goroutines
	var consWg sync.WaitGroup // tracks consumer goroutines

	var totalConsumed int
	var mu sync.Mutex

	// Start consumers first so they are ready when producers start sending.
	for i := 0; i < numConsumers; i++ {
		consWg.Add(1)
		go consumer(i+1, queue, &totalConsumed, &mu, &consWg)
	}

	// Start producers; first 'remainder' producers get one extra item.
	for i := 0; i < numProducers; i++ {
		count := base
		if i < remainder {
			count++
		}
		prodWg.Add(1)
		go producer(i+1, count, queue, &prodWg)
	}

	// Wait for all producers to finish, then close the queue.
	prodWg.Wait()
	close(queue)
	fmt.Println("All producers done, queue closed.")

	// Wait for all consumers to drain the queue.
	consWg.Wait()

	fmt.Printf("\nTotal produced: %d\n", totalItems)
	fmt.Printf("Total consumed: %d\n", totalConsumed)
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	demo()
}
