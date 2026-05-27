package advanced_routing

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type CacheConfig struct {
	Enabled          bool          `json:"enabled"`
	DefaultTTL       time.Duration `json:"default_ttl"`
	MaxTTL           time.Duration `json:"max_ttl"`
	MaxSize          int64         `json:"max_size"`    // Maximum cache size in bytes
	MaxEntries       int           `json:"max_entries"` // Maximum number of cache entries
	CacheableMethods []string      `json:"cacheable_methods"`
	CacheableStatus  []int         `json:"cacheable_status"`
	SkipCacheHeaders []string      `json:"skip_cache_headers"`
	VaryHeaders      []string      `json:"vary_headers"`
	KeyGenerator     string        `json:"key_generator"` // "url", "url+headers", "custom"
	Compression      bool          `json:"compression"`
}

type CacheEntry struct {
	Key          string            `json:"key"`
	StatusCode   int               `json:"status_code"`
	Headers      map[string]string `json:"headers"`
	Body         []byte            `json:"body"`
	ContentType  string            `json:"content_type"`
	ETag         string            `json:"etag"`
	LastModified string            `json:"last_modified"`
	ExpiresAt    time.Time         `json:"expires_at"`
	CreatedAt    time.Time         `json:"created_at"`
	AccessCount  int64             `json:"access_count"`
	Size         int64             `json:"size"`
	Vary         map[string]string `json:"vary"`
}

type ResponseCache struct {
	config    CacheConfig
	entries   map[string]*CacheEntry
	size      int64
	mu        sync.RWMutex
	stats     CacheStats
	cleanupCh chan struct{}
}

type CacheStats struct {
	Hits        int64     `json:"hits"`
	Misses      int64     `json:"misses"`
	Sets        int64     `json:"sets"`
	Evictions   int64     `json:"evictions"`
	Size        int64     `json:"size"`
	Entries     int       `json:"entries"`
	HitRatio    float64   `json:"hit_ratio"`
	LastCleanup time.Time `json:"last_cleanup"`
}

func NewResponseCache(config CacheConfig) *ResponseCache {
	// Set default values
	if config.DefaultTTL == 0 {
		config.DefaultTTL = 5 * time.Minute
	}
	if config.MaxTTL == 0 {
		config.MaxTTL = 24 * time.Hour
	}
	if config.MaxSize == 0 {
		config.MaxSize = 100 * 1024 * 1024 // 100MB
	}
	if config.MaxEntries == 0 {
		config.MaxEntries = 10000
	}
	if len(config.CacheableMethods) == 0 {
		config.CacheableMethods = []string{"GET", "HEAD"}
	}
	if len(config.CacheableStatus) == 0 {
		config.CacheableStatus = []int{200, 203, 300, 301, 302, 307, 410}
	}
	if len(config.SkipCacheHeaders) == 0 {
		config.SkipCacheHeaders = []string{"Authorization", "Cookie", "Set-Cookie"}
	}
	if len(config.VaryHeaders) == 0 {
		config.VaryHeaders = []string{"Accept", "Accept-Encoding", "Accept-Language"}
	}
	if config.KeyGenerator == "" {
		config.KeyGenerator = "url+headers"
	}

	cache := &ResponseCache{
		config:    config,
		entries:   make(map[string]*CacheEntry),
		cleanupCh: make(chan struct{}),
		stats:     CacheStats{LastCleanup: time.Now()},
	}

	// Start cleanup goroutine
	go cache.cleanupRoutine()

	return cache
}

func (rc *ResponseCache) Get(key string) *CacheEntry {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	entry, exists := rc.entries[key]
	if !exists {
		rc.stats.Misses++
		return nil
	}

	// Check if entry has expired
	if time.Now().After(entry.ExpiresAt) {
		rc.mu.RUnlock()
		rc.mu.Lock()
		delete(rc.entries, key)
		rc.size -= entry.Size
		rc.stats.Evictions++
		rc.mu.Unlock()
		rc.mu.RLock()
		rc.stats.Misses++
		return nil
	}

	entry.AccessCount++
	rc.stats.Hits++
	rc.updateHitRatio()

	return entry
}

func (rc *ResponseCache) Set(key string, resp *http.Response, body []byte) *CacheEntry {
	if !rc.isCacheable(resp) {
		return nil
	}

	// Determine TTL
	ttl := rc.getTTL(resp)
	if ttl <= 0 {
		return nil
	}

	// Create cache entry
	entry := &CacheEntry{
		Key:          key,
		StatusCode:   resp.StatusCode,
		Headers:      make(map[string]string),
		Body:         body,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ExpiresAt:    time.Now().Add(ttl),
		CreatedAt:    time.Now(),
		AccessCount:  0,
		Size:         int64(len(body)),
		Vary:         make(map[string]string),
	}

	// Copy relevant headers
	for name, values := range resp.Header {
		if !rc.shouldSkipHeader(name) {
			entry.Headers[name] = strings.Join(values, ", ")
		}
	}

	// Store Vary header values
	varyValues := resp.Header.Values("Vary")
	for _, vary := range varyValues {
		varyHeaders := strings.Split(vary, ",")
		for _, header := range varyHeaders {
			header = strings.TrimSpace(header)
			if values := resp.Header.Values(header); len(values) > 0 {
				entry.Vary[header] = strings.Join(values, ", ")
			}
		}
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Check if we need to evict entries
	rc.ensureCapacity(entry.Size)

	// Store entry
	rc.entries[key] = entry
	rc.size += entry.Size
	rc.stats.Sets++
	rc.stats.Size = rc.size
	rc.stats.Entries = len(rc.entries)

	return entry
}

func (rc *ResponseCache) isCacheable(resp *http.Response) bool {
	// Check method
	method := resp.Request.Method
	methodCacheable := false
	for _, cacheableMethod := range rc.config.CacheableMethods {
		if method == cacheableMethod {
			methodCacheable = true
			break
		}
	}
	if !methodCacheable {
		return false
	}

	// Check status code
	statusCacheable := false
	for _, cacheableStatus := range rc.config.CacheableStatus {
		if resp.StatusCode == cacheableStatus {
			statusCacheable = true
			break
		}
	}
	if !statusCacheable {
		return false
	}

	// Check cache control headers
	cacheControl := resp.Header.Get("Cache-Control")
	if strings.Contains(cacheControl, "no-cache") || strings.Contains(cacheControl, "private") {
		return false
	}

	// Check authorization
	if resp.Request.Header.Get("Authorization") != "" {
		return false
	}

	return true
}

func (rc *ResponseCache) getTTL(resp *http.Response) time.Duration {
	// Check Cache-Control header
	cacheControl := resp.Header.Get("Cache-Control")

	// Extract max-age
	if strings.Contains(cacheControl, "max-age=") {
		parts := strings.Split(cacheControl, "max-age=")
		if len(parts) > 1 {
			maxAge := strings.Split(parts[1], ",")[0]
			maxAge = strings.TrimSpace(maxAge)
			if seconds, err := time.ParseDuration(maxAge + "s"); err == nil {
				if seconds > rc.config.MaxTTL {
					return rc.config.MaxTTL
				}
				return seconds
			}
		}
	}

	// Check Expires header
	if expires := resp.Header.Get("Expires"); expires != "" {
		if expiryTime, err := time.Parse(time.RFC1123, expires); err == nil {
			ttl := time.Until(expiryTime)
			if ttl > rc.config.MaxTTL {
				return rc.config.MaxTTL
			}
			if ttl > 0 {
				return ttl
			}
		}
	}

	// Use default TTL
	return rc.config.DefaultTTL
}

func (rc *ResponseCache) shouldSkipHeader(name string) bool {
	for _, skipHeader := range rc.config.SkipCacheHeaders {
		if strings.EqualFold(name, skipHeader) {
			return true
		}
	}
	return false
}

func (rc *ResponseCache) ensureCapacity(entrySize int64) {
	// Check size limit
	for rc.size+entrySize > rc.config.MaxSize && len(rc.entries) > 0 {
		rc.evictLRU()
	}

	// Check entry limit
	for len(rc.entries) >= rc.config.MaxEntries && len(rc.entries) > 0 {
		rc.evictLRU()
	}
}

func (rc *ResponseCache) evictLRU() {
	var oldestKey string
	var oldestTime time.Time
	var oldestAccess int64 = int64(^uint64(0) >> 1) // Max int64

	for key, entry := range rc.entries {
		if entry.AccessCount < oldestAccess ||
			(entry.AccessCount == oldestAccess && entry.CreatedAt.Before(oldestTime)) {
			oldestKey = key
			oldestTime = entry.CreatedAt
			oldestAccess = entry.AccessCount
		}
	}

	if oldestKey != "" {
		if entry, exists := rc.entries[oldestKey]; exists {
			delete(rc.entries, oldestKey)
			rc.size -= entry.Size
			rc.stats.Evictions++
		}
	}
}

func (rc *ResponseCache) GenerateKey(r *http.Request) string {
	switch rc.config.KeyGenerator {
	case "url":
		return rc.generateURLKey(r)
	case "url+headers":
		return rc.generateURLWithHeadersKey(r)
	case "custom":
		return rc.generateCustomKey(r)
	default:
		return rc.generateURLWithHeadersKey(r)
	}
}

func (rc *ResponseCache) generateURLKey(r *http.Request) string {
	return r.URL.String()
}

func (rc *ResponseCache) generateURLWithHeadersKey(r *http.Request) string {
	key := r.URL.String()

	// Add vary headers to key
	for _, header := range rc.config.VaryHeaders {
		if values := r.Header.Values(header); len(values) > 0 {
			key += "|" + header + ":" + strings.Join(values, ",")
		}
	}

	return key
}

func (rc *ResponseCache) generateCustomKey(r *http.Request) string {
	// Custom key generation including method, URL, and relevant headers
	h := md5.New()
	h.Write([]byte(r.Method))
	h.Write([]byte(r.URL.String()))

	for _, header := range rc.config.VaryHeaders {
		if values := r.Header.Values(header); len(values) > 0 {
			h.Write([]byte(header + ":" + strings.Join(values, ",")))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

func (rc *ResponseCache) updateHitRatio() {
	total := rc.stats.Hits + rc.stats.Misses
	if total > 0 {
		rc.stats.HitRatio = float64(rc.stats.Hits) / float64(total)
	}
}

func (rc *ResponseCache) cleanupRoutine() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rc.cleanup()
		case <-rc.cleanupCh:
			return
		}
	}
}

func (rc *ResponseCache) cleanup() {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	now := time.Now()
	for key, entry := range rc.entries {
		if now.After(entry.ExpiresAt) {
			delete(rc.entries, key)
			rc.size -= entry.Size
			rc.stats.Evictions++
		}
	}

	rc.stats.LastCleanup = time.Now()
	rc.stats.Size = rc.size
	rc.stats.Entries = len(rc.entries)
}

func (rc *ResponseCache) GetStats() CacheStats {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	stats := rc.stats
	stats.Size = rc.size
	stats.Entries = len(rc.entries)
	return stats
}

func (rc *ResponseCache) Clear() {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rc.entries = make(map[string]*CacheEntry)
	rc.size = 0
	rc.stats = CacheStats{LastCleanup: time.Now()}
}

func (rc *ResponseCache) Stop() {
	close(rc.cleanupCh)
}

// CachedResponseWriter captures response for caching
type CachedResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       []byte
	headers    map[string][]string
}

func NewCachedResponseWriter(w http.ResponseWriter) *CachedResponseWriter {
	return &CachedResponseWriter{
		ResponseWriter: w,
		headers:        make(map[string][]string),
		statusCode:     http.StatusOK, // Default status code
	}
}

func (crw *CachedResponseWriter) WriteHeader(statusCode int) {
	crw.statusCode = statusCode
	crw.ResponseWriter.WriteHeader(statusCode)
}

func (crw *CachedResponseWriter) Write(b []byte) (int, error) {
	crw.body = append(crw.body, b...)
	return crw.ResponseWriter.Write(b)
}

func (crw *CachedResponseWriter) Header() http.Header {
	return crw.ResponseWriter.Header()
}

func (crw *CachedResponseWriter) GetResponse() (*http.Response, []byte) {
	resp := &http.Response{
		StatusCode: crw.statusCode,
		Header:     make(http.Header),
		Request:    nil, // Will be set by caller
	}

	// Copy headers
	for k, v := range crw.headers {
		resp.Header[k] = v
	}

	return resp, crw.body
}

// CacheMiddleware provides caching middleware
type CacheMiddleware struct {
	cache *ResponseCache
	next  http.Handler
}

func NewCacheMiddleware(cache *ResponseCache, next http.Handler) *CacheMiddleware {
	return &CacheMiddleware{
		cache: cache,
		next:  next,
	}
}

func (cm *CacheMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !cm.cache.config.Enabled {
		cm.next.ServeHTTP(w, r)
		return
	}

	// Generate cache key
	key := cm.cache.GenerateKey(r)

	// Try to get from cache
	if entry := cm.cache.Get(key); entry != nil {
		// Check if client has fresh version
		if ifNoneMatch := r.Header.Get("If-None-Match"); ifNoneMatch != "" && ifNoneMatch == entry.ETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		if ifModifiedSince := r.Header.Get("If-Modified-Since"); ifModifiedSince != "" {
			if lastModified, err := time.Parse(time.RFC1123, entry.LastModified); err == nil {
				if ifModifiedSinceTime, err := time.Parse(time.RFC1123, ifModifiedSince); err == nil {
					if !lastModified.After(ifModifiedSinceTime) {
						w.WriteHeader(http.StatusNotModified)
						return
					}
				}
			}
		}

		// Serve cached response
		for name, value := range entry.Headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(entry.StatusCode)
		w.Write(entry.Body)
		return
	}

	// Capture response for caching
	crw := NewCachedResponseWriter(w)
	cm.next.ServeHTTP(crw, r)

	// Cache the response
	resp, body := crw.GetResponse()
	resp.Request = r
	cm.cache.Set(key, resp, body)
}

// CacheInvalidator handles cache invalidation
type CacheInvalidator struct {
	cache *ResponseCache
	mu    sync.RWMutex
}

func NewCacheInvalidator(cache *ResponseCache) *CacheInvalidator {
	return &CacheInvalidator{
		cache: cache,
	}
}

func (ci *CacheInvalidator) InvalidatePattern(pattern string) {
	ci.mu.Lock()
	defer ci.mu.Unlock()

	// Simple pattern matching - in practice, you'd use regex
	for key := range ci.cache.entries {
		if strings.Contains(key, pattern) {
			ci.cache.mu.Lock()
			if entry, exists := ci.cache.entries[key]; exists {
				delete(ci.cache.entries, key)
				ci.cache.size -= entry.Size
				ci.cache.stats.Evictions++
			}
			ci.cache.mu.Unlock()
		}
	}
}

func (ci *CacheInvalidator) InvalidateURL(url string) {
	ci.InvalidatePattern(url)
}

func (ci *CacheInvalidator) InvalidateBackend(backend backend.Backend) {
	backendURL := backend.GetURL().String()
	ci.InvalidatePattern(backendURL)
}
