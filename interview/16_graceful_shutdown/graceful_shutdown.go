package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Active request tracking
// ---------------------------------------------------------------------------

var wg sync.WaitGroup

// activeMiddleware increments WaitGroup before the handler runs and
// decrements after it returns. This lets us wait for in-flight requests
// during graceful shutdown.
func activeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wg.Add(1)
		defer wg.Done()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// slowHandler simulates a long-running request (e.g. database query,
// external API call). It sleeps for the requested duration, then returns
// a 200 response.
func slowHandler(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("dur")
	if d == "" {
		d = "2s"
	}
	dur, err := time.ParseDuration(d)
	if err != nil {
		dur = 2 * time.Second
	}

	select {
	case <-time.After(dur):
		fmt.Fprintf(w, "ok: request completed after %v\n", dur)
	case <-r.Context().Done():
		// Server shutdown cancelled this request.
		fmt.Fprintf(w, "cancelled: request interrupted\n")
	}
}

// healthHandler returns a quick health-check response.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "healthy\n")
}

// ---------------------------------------------------------------------------
// Server setup
// ---------------------------------------------------------------------------

// newServer builds an *http.Server with our routes wrapped in the
// active-request tracking middleware.
func newServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", slowHandler)
	mux.HandleFunc("/health", healthHandler)

	return &http.Server{
		Addr:    addr,
		Handler: activeMiddleware(mux),
	}
}

// ---------------------------------------------------------------------------
// Graceful shutdown
// ---------------------------------------------------------------------------

// listenForSignal blocks until SIGINT or SIGTERM is received, then sends
// on the returned channel. Bidirectional so the demo can simulate a signal.
func listenForSignal() chan os.Signal {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt) // SIGINT (Ctrl-C) and SIGTERM
	return sig
}

// drainAndShutdown waits for active requests to finish (up to timeout),
// then shuts down the server. Returns the error from Shutdown (if any).
func drainAndShutdown(srv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Wait for in-flight requests to complete.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait until either all requests finish or the timeout expires.
	select {
	case <-done:
		fmt.Println("  [drain] all in-flight requests completed")
	case <-ctx.Done():
		fmt.Printf("  [drain] timeout after %v, forcing shutdown\n", timeout)
	}

	return srv.Shutdown(ctx)
}

// ---------------------------------------------------------------------------
// Demo — automatic shutdown after 3 seconds
// ---------------------------------------------------------------------------

func demo() {
	srv := newServer(":18080")
	sigCh := listenForSignal()

	// Also set up a timer to simulate a signal after 3 seconds (for demo).
	demoTimer := time.AfterFunc(3*time.Second, func() {
		fmt.Println("\n  [demo] simulating SIGINT after 3s...")
		sigCh <- os.Interrupt
	})

	fmt.Println("=== Graceful Shutdown Demo ===")
	fmt.Println("Server starting on :18080")
	fmt.Println("  Send SIGINT (Ctrl-C) to trigger shutdown")
	fmt.Println()

	// Start the real server in a goroutine.
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("  [error] server: %v\n", err)
		}
	}()

	fmt.Println("  [server] listening on :18080")

	// Launch a few fake slow requests to show draining behavior.
	fmt.Println("  [demo] launching 2 slow requests (1.5s and 4s)...")

	// Simulate in-flight requests by directly manipulating the WaitGroup.
	go func() {
		wg.Add(1)
		fmt.Println("  [req-1] started (will take 1.5s)")
		time.Sleep(1500 * time.Millisecond)
		fmt.Println("  [req-1] finished")
		wg.Done()
	}()

	go func() {
		wg.Add(1)
		fmt.Println("  [req-2] started (will take 4s — will be cancelled)")
		time.Sleep(4000 * time.Millisecond)
		fmt.Println("  [req-2] finished")
		wg.Done()
	}()

	// Block until signal received.
	select {
	case s := <-sigCh:
		fmt.Printf("\n  [signal] received %v — shutting down...\n", s)
	}

	// Stop the demo timer (already fired, but good practice).
	demoTimer.Stop()

	// Drain in-flight requests with a 10-second timeout.
	fmt.Println("  [shutdown] draining active requests (timeout=10s)...")
	start := time.Now()
	err := drainAndShutdown(srv, 10*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("  [shutdown] error: %v (elapsed: %v)\n", err, elapsed)
	} else {
		fmt.Printf("  [shutdown] clean shutdown (elapsed: %v)\n", elapsed)
	}

	fmt.Println("\n=== Demo Complete ===")
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	demo()
}
