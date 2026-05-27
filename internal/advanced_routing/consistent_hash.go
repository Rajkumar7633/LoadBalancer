package advanced_routing

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"loadbalancer/internal/backend"
)

type ConsistentHash struct {
	hash     func(data []byte) uint32
	replicas int
	keys     []int // Sorted
	hashMap  map[int]backend.Backend
	mu       sync.RWMutex
}

func NewConsistentHash(replicas int, fn func([]byte) uint32) *ConsistentHash {
	ch := &ConsistentHash{
		replicas: replicas,
		hash:     fn,
		hashMap:  make(map[int]backend.Backend),
	}
	if ch.hash == nil {
		ch.hash = crc32Hash
	}
	return ch
}

func (ch *ConsistentHash) Add(backends ...backend.Backend) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	for _, backend := range backends {
		for i := 0; i < ch.replicas; i++ {
			hash := int(ch.hash([]byte(strconv.Itoa(i) + backend.GetURL().String())))
			ch.keys = append(ch.keys, hash)
			ch.hashMap[hash] = backend
		}
	}
	sort.Ints(ch.keys)
}

func (ch *ConsistentHash) Remove(backend backend.Backend) {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	for i := 0; i < ch.replicas; i++ {
		hash := int(ch.hash([]byte(strconv.Itoa(i) + backend.GetURL().String())))
		index := sort.SearchInts(ch.keys, hash)
		if index < len(ch.keys) && ch.keys[index] == hash {
			ch.keys = append(ch.keys[:index], ch.keys[index+1:]...)
		}
		delete(ch.hashMap, hash)
	}
}

func (ch *ConsistentHash) Get(key string) backend.Backend {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	if len(ch.keys) == 0 {
		return nil
	}

	hash := int(ch.hash([]byte(key)))
	index := sort.SearchInts(ch.keys, hash)

	// Wrap around if we reach the end
	if index == len(ch.keys) {
		index = 0
	}

	return ch.hashMap[ch.keys[index]]
}

func crc32Hash(data []byte) uint32 {
	// Simple hash function using SHA256 and taking first 4 bytes
	hash := sha256.Sum256(data)
	return uint32(hash[0])<<24 | uint32(hash[1])<<16 | uint32(hash[2])<<8 | uint32(hash[3])
}

// SessionAffinityRouter provides consistent hashing based session affinity
type SessionAffinityRouter struct {
	hash     *ConsistentHash
	keyFunc  func(*http.Request) string
	fallback []backend.Backend
}

func NewSessionAffinityRouter(replicas int, keyFunc func(*http.Request) string) *SessionAffinityRouter {
	return &SessionAffinityRouter{
		hash:    NewConsistentHash(replicas, nil),
		keyFunc: keyFunc,
	}
}

func (sar *SessionAffinityRouter) SetBackends(backends []backend.Backend) {
	sar.hash = NewConsistentHash(sar.hash.replicas, nil)
	sar.hash.Add(backends...)
	sar.fallback = backends
}

func (sar *SessionAffinityRouter) Route(r *http.Request) backend.Backend {
	key := sar.keyFunc(r)
	if key == "" {
		// Fallback to round-robin if no key
		return sar.fallback[0]
	}

	backend := sar.hash.Get(key)
	if backend == nil {
		// Fallback if hash ring is empty
		if len(sar.fallback) > 0 {
			return sar.fallback[0]
		}
		return nil
	}

	return backend
}

// Key extraction functions
func UserIDKeyFunc(r *http.Request) string {
	// Try to get user ID from various sources
	if userID := r.Header.Get("X-User-ID"); userID != "" {
		return userID
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		return userID
	}
	if sessionID := r.Header.Get("X-Session-ID"); sessionID != "" {
		return sessionID
	}
	if cookie, err := r.Cookie("session_id"); err == nil {
		return cookie.Value
	}
	return ""
}

func APIKeyFunc(r *http.Request) string {
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		return apiKey
	}
	if apiKey := r.URL.Query().Get("api_key"); apiKey != "" {
		return apiKey
	}
	return ""
}

func JWTKeyFunc(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		// Extract JWT token and hash it for consistency
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && parts[0] == "Bearer" {
			hash := sha256.Sum256([]byte(parts[1]))
			return hex.EncodeToString(hash[:])
		}
	}
	return ""
}
