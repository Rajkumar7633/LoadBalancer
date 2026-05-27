package advanced_routing

import (
	"crypto/sha256"
	"net"
	"net/http"
	"strconv"
	"sync"

	"loadbalancer/internal/backend"
)

type IPHashRouter struct {
	backends []backend.Backend
	mu       sync.RWMutex
}

func NewIPHashRouter() *IPHashRouter {
	return &IPHashRouter{
		backends: make([]backend.Backend, 0),
	}
}

func (ihr *IPHashRouter) SetBackends(backends []backend.Backend) {
	ihr.mu.Lock()
	defer ihr.mu.Unlock()
	ihr.backends = backends
}

func (ihr *IPHashRouter) Route(r *http.Request) backend.Backend {
	ihr.mu.RLock()
	defer ihr.mu.RUnlock()

	if len(ihr.backends) == 0 {
		return nil
	}

	clientIP := getClientIP(r)
	if clientIP == "" {
		// Fallback to first backend if no IP
		return ihr.backends[0]
	}

	hash := ipHash(clientIP)
	index := hash % len(ihr.backends)

	// Ensure backend is healthy
	backend := ihr.backends[index]
	if backend.IsHealthy() && !backend.IsDraining() {
		return backend
	}

	// Fallback to first healthy backend
	for _, b := range ihr.backends {
		if b.IsHealthy() && !b.IsDraining() {
			return b
		}
	}

	return nil
}

func ipHash(ip string) int {
	hash := sha256.Sum256([]byte(ip))
	// Use first 4 bytes to create an integer
	result := int(hash[0])<<24 | int(hash[1])<<16 | int(hash[2])<<8 | int(hash[3])
	if result < 0 {
		result = -result
	}
	return result
}

func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if idx := len(xff); idx > 0 {
			for i, c := range xff {
				if c == ',' {
					return xff[:i]
				}
			}
			return xff
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Check Cloudflare headers
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		return cf
	}

	// Parse RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}

// WeightedIPHashRouter combines IP hashing with weighted selection
type WeightedIPHashRouter struct {
	backends []backend.Backend
	mu       sync.RWMutex
}

func NewWeightedIPHashRouter() *WeightedIPHashRouter {
	return &WeightedIPHashRouter{
		backends: make([]backend.Backend, 0),
	}
}

func (wir *WeightedIPHashRouter) SetBackends(backends []backend.Backend) {
	wir.mu.Lock()
	defer wir.mu.Unlock()
	wir.backends = backends
}

func (wir *WeightedIPHashRouter) Route(r *http.Request) backend.Backend {
	wir.mu.RLock()
	defer wir.mu.RUnlock()

	if len(wir.backends) == 0 {
		return nil
	}

	clientIP := getClientIP(r)
	if clientIP == "" {
		// Fallback to weighted selection
		return wir.weightedSelection()
	}

	// Create weighted list based on healthy backends
	healthyBackends := make([]backend.Backend, 0)
	for _, backend := range wir.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			healthyBackends = append(healthyBackends, backend)
		}
	}

	if len(healthyBackends) == 0 {
		return nil
	}

	// Create weighted list
	weightedList := make([]backend.Backend, 0)
	for _, backend := range healthyBackends {
		weight := backend.GetWeight()
		for i := 0; i < weight; i++ {
			weightedList = append(weightedList, backend)
		}
	}

	if len(weightedList) == 0 {
		return healthyBackends[0]
	}

	hash := ipHash(clientIP)
	index := hash % len(weightedList)

	return weightedList[index]
}

func (wir *WeightedIPHashRouter) weightedSelection() backend.Backend {
	healthyBackends := make([]backend.Backend, 0)
	for _, backend := range wir.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			healthyBackends = append(healthyBackends, backend)
		}
	}

	if len(healthyBackends) == 0 {
		return nil
	}

	// Create weighted list
	weightedList := make([]backend.Backend, 0)
	for _, backend := range healthyBackends {
		weight := backend.GetWeight()
		for i := 0; i < weight; i++ {
			weightedList = append(weightedList, backend)
		}
	}

	if len(weightedList) == 0 {
		return healthyBackends[0]
	}

	// Simple random selection from weighted list
	hash := ipHash(strconv.FormatInt(int64(len(weightedList)), 10))
	index := hash % len(weightedList)

	return weightedList[index]
}
