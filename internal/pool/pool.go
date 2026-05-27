package pool

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"loadbalancer/internal/config"
	"loadbalancer/internal/logger"
)

type Pool struct {
	config    config.PoolConfig
	logger    *logger.Logger
	clients   chan *http.Client
	active    int
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
}

type PooledClient struct {
	client   *http.Client
	lastUsed time.Time
	inUse    bool
	useCount int
	mu       sync.Mutex
}

func NewPool(cfg config.PoolConfig, logger *logger.Logger) *Pool {
	pool := &Pool{
		config:  cfg,
		logger:  logger,
		clients: make(chan *http.Client, cfg.MaxIdleConns),
		closed:  false,
	}

	// Pre-warm the pool with some clients
	go pool.preWarm()

	return pool
}

func (p *Pool) preWarm() {
	// Create initial clients
	for i := 0; i < p.config.MaxIdleConnsPerHost; i++ {
		client := p.createClient()
		select {
		case p.clients <- client:
		default:
			break
		}
	}
}

func (p *Pool) createClient() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        p.config.MaxIdleConns,
		MaxIdleConnsPerHost: p.config.MaxIdleConnsPerHost,
		IdleConnTimeout:     p.config.IdleConnTimeout,
		DisableCompression:  false,
		ForceAttemptHTTP2:   true,
		// Custom dialer for connection reuse
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// TLS configuration for optimal performance
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}
}

func (p *Pool) GetClient() *http.Client {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return p.createClient() // Return new client if pool is closed
	}
	p.mu.RUnlock()

	select {
	case client := <-p.clients:
		p.mu.Lock()
		p.active++
		p.mu.Unlock()
		return client
	default:
		// No available clients in pool, create a new one
		p.mu.Lock()
		p.active++
		p.mu.Unlock()
		return p.createClient()
	}
}

func (p *Pool) PutClient(client *http.Client) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return // Don't put clients back if pool is closed
	}

	p.active--

	select {
	case p.clients <- client:
		// Client returned to pool successfully
	default:
		// Pool is full, discard the client
		p.logger.Debug("Connection pool full, discarding client")
	}
}

func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()

		// Close all idle connections
		close(p.clients)
		for client := range p.clients {
			if transport, ok := client.Transport.(*http.Transport); ok {
				transport.CloseIdleConnections()
			}
		}
	})
}

func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return PoolStats{
		ActiveConnections: p.active,
		IdleConnections:   len(p.clients),
		MaxConnections:    p.config.MaxIdleConns,
		Closed:            p.closed,
	}
}

type PoolStats struct {
	ActiveConnections int
	IdleConnections   int
	MaxConnections    int
	Closed            bool
}

// Advanced connection pooling features

type AdvancedPool struct {
	*Pool
	perHostPools map[string]*HostPool
	mu           sync.RWMutex
}

type HostPool struct {
	host    string
	clients chan *http.Client
	config  config.PoolConfig
	mu      sync.RWMutex
	closed  bool
}

func NewAdvancedPool(cfg config.PoolConfig, logger *logger.Logger) *AdvancedPool {
	basePool := NewPool(cfg, logger)

	return &AdvancedPool{
		Pool:         basePool,
		perHostPools: make(map[string]*HostPool),
	}
}

func (ap *AdvancedPool) GetClientForHost(host string) *http.Client {
	ap.mu.RLock()
	hostPool, exists := ap.perHostPools[host]
	ap.mu.RUnlock()

	if !exists {
		ap.mu.Lock()
		// Double-check after acquiring write lock
		hostPool, exists = ap.perHostPools[host]
		if !exists {
			hostPool = &HostPool{
				host:    host,
				clients: make(chan *http.Client, ap.config.MaxIdleConnsPerHost),
				config:  ap.config,
				closed:  false,
			}
			ap.perHostPools[host] = hostPool

			// Pre-warm this host pool
			go ap.preWarmHostPool(hostPool)
		}
		ap.mu.Unlock()
	}

	return hostPool.getClient()
}

func (ap *AdvancedPool) PutClientForHost(host string, client *http.Client) {
	ap.mu.RLock()
	hostPool, exists := ap.perHostPools[host]
	ap.mu.RUnlock()

	if !exists {
		return // No pool for this host, discard client
	}

	hostPool.putClient(client)
}

func (ap *AdvancedPool) preWarmHostPool(hostPool *HostPool) {
	for i := 0; i < ap.config.MaxIdleConnsPerHost/2; i++ {
		client := ap.createClient()
		select {
		case hostPool.clients <- client:
		default:
			break
		}
	}
}

func (hp *HostPool) getClient() *http.Client {
	hp.mu.RLock()
	if hp.closed {
		hp.mu.RUnlock()
		return &http.Client{}
	}
	hp.mu.RUnlock()

	select {
	case client := <-hp.clients:
		return client
	default:
		// Create new client for this host
		transport := &http.Transport{
			MaxIdleConns:        hp.config.MaxIdleConns,
			MaxIdleConnsPerHost: hp.config.MaxIdleConnsPerHost,
			IdleConnTimeout:     hp.config.IdleConnTimeout,
			DisableCompression:  false,
			ForceAttemptHTTP2:   true,
		}

		return &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
		}
	}
}

func (hp *HostPool) putClient(client *http.Client) {
	hp.mu.RLock()
	defer hp.mu.RUnlock()

	if hp.closed {
		return
	}

	select {
	case hp.clients <- client:
	default:
		// Pool is full, discard
	}
}

func (hp *HostPool) close() {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	hp.closed = true
	close(hp.clients)
	for client := range hp.clients {
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
}

func (ap *AdvancedPool) Close() {
	ap.Pool.Close()

	ap.mu.Lock()
	defer ap.mu.Unlock()

	for _, hostPool := range ap.perHostPools {
		hostPool.close()
	}
	ap.perHostPools = make(map[string]*HostPool)
}

func (ap *AdvancedPool) GetStats() map[string]PoolStats {
	ap.mu.RLock()
	defer ap.mu.RUnlock()

	stats := make(map[string]PoolStats)

	// Global pool stats
	stats["global"] = ap.Pool.Stats()

	// Per-host pool stats
	for host, hostPool := range ap.perHostPools {
		hostPool.mu.RLock()
		stats[host] = PoolStats{
			ActiveConnections: 0, // Not tracked at host level
			IdleConnections:   len(hostPool.clients),
			MaxConnections:    hostPool.config.MaxIdleConnsPerHost,
			Closed:            hostPool.closed,
		}
		hostPool.mu.RUnlock()
	}

	return stats
}

// Connection health monitoring

type HealthMonitoringPool struct {
	*AdvancedPool
	healthChecker   func(*http.Client) bool
	checkInterval   time.Duration
	lastHealthCheck map[string]time.Time
	mu              sync.RWMutex
}

func NewHealthMonitoringPool(cfg config.PoolConfig, logger *logger.Logger) *HealthMonitoringPool {
	advancedPool := NewAdvancedPool(cfg, logger)

	return &HealthMonitoringPool{
		AdvancedPool:    advancedPool,
		healthChecker:   defaultHealthChecker,
		checkInterval:   5 * time.Minute,
		lastHealthCheck: make(map[string]time.Time),
	}
}

func defaultHealthChecker(client *http.Client) bool {
	// Basic health check - try to make a simple request
	resp, err := client.Get("http://httpbin.org/status/200")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func (hmp *HealthMonitoringPool) GetClient() *http.Client {
	client := hmp.AdvancedPool.GetClient()

	// Check if client needs health verification
	if hmp.shouldCheckHealth(client) {
		go hmp.verifyClientHealth(client)
	}

	return client
}

func (hmp *HealthMonitoringPool) shouldCheckHealth(client *http.Client) bool {
	hmp.mu.RLock()
	defer hmp.mu.RUnlock()

	clientKey := fmt.Sprintf("%p", client)
	lastCheck, exists := hmp.lastHealthCheck[clientKey]

	return !exists || time.Since(lastCheck) > hmp.checkInterval
}

func (hmp *HealthMonitoringPool) verifyClientHealth(client *http.Client) {
	if !hmp.healthChecker(client) {
		// Client is unhealthy, don't return it to pool
		hmp.logger.Warn("Unhealthy client detected, discarding")
		return
	}

	hmp.mu.Lock()
	clientKey := fmt.Sprintf("%p", client)
	hmp.lastHealthCheck[clientKey] = time.Now()
	hmp.mu.Unlock()
}
