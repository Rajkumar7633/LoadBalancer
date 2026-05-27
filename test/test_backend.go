package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"
)

func main() {
	// Get port from command line or default to 8081
	port := "8081"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	// Get backend ID from command line or default to 1
	backendID := "1"
	if len(os.Args) > 2 {
		backendID = os.Args[2]
	}

	rand.Seed(time.Now().UnixNano())

	// Health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Simulate occasional health check failures
		if rand.Intn(100) < 5 { // 5% failure rate
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("UNHEALTHY"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Main endpoint
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Simulate variable response times
		delay := time.Duration(rand.Intn(100)) * time.Millisecond
		time.Sleep(delay)

		response := fmt.Sprintf("Backend %s on port %s - Time: %s, Delay: %v\n",
			backendID, port, time.Now().Format(time.RFC3339), delay)

		w.Header().Set("X-Backend-ID", backendID)
		w.Header().Set("X-Backend-Port", port)
		w.Header().Set("X-Response-Time", delay.String())

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	})

	// API endpoint for testing
	http.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]interface{}{
			"backend_id": backendID,
			"port":       port,
			"timestamp":  time.Now().Unix(),
			"request_id": rand.Intn(100000),
			"status":     "success",
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"backend_id":"%s","port":"%s","timestamp":%d,"request_id":%d,"status":"success"}`,
			data["backend_id"], data["port"], data["timestamp"], data["request_id"])
	})

	// Slow endpoint for testing timeouts
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // Very slow response
		w.Write([]byte("This is a very slow response\n"))
	})

	// Error endpoint for testing circuit breaker
	http.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		if rand.Intn(2) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Internal Server Error"))
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Success"))
		}
	})

	log.Printf("Test backend %s starting on port %s", backendID, port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
