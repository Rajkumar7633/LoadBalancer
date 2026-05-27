package advanced_routing

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"loadbalancer/internal/backend"
)

type WebSocketConfig struct {
	Enabled           bool          `json:"enabled"`
	OriginCheck       bool          `json:"origin_check"`
	AllowedOrigins    []string      `json:"allowed_origins"`
	PingInterval      time.Duration `json:"ping_interval"`
	PongWait          time.Duration `json:"pong_wait"`
	WriteWait         time.Duration `json:"write_wait"`
	MaxMessageSize    int64         `json:"max_message_size"`
	ReadBufferSize    int           `json:"read_buffer_size"`
	WriteBufferSize   int           `json:"write_buffer_size"`
	EnableCompression bool          `json:"enable_compression"`
}

type WebSocketConnection struct {
	conn         *websocket.Conn
	backend      backend.Backend
	backendConn  *websocket.Conn
	isConnected  bool
	lastActivity time.Time
	mu           sync.RWMutex
}

type WebSocketRouter struct {
	backends       []backend.Backend
	config         WebSocketConfig
	connections    map[string]*WebSocketConnection
	connectionPool map[string][]*websocket.Conn // Backend -> connections pool
	mu             sync.RWMutex
	upgrader       websocket.Upgrader
}

func NewWebSocketRouter(config WebSocketConfig) *WebSocketRouter {
	// Set default values
	if config.PingInterval == 0 {
		config.PingInterval = 54 * time.Second
	}
	if config.PongWait == 0 {
		config.PongWait = 60 * time.Second
	}
	if config.WriteWait == 0 {
		config.WriteWait = 10 * time.Second
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = 512 << 10 // 512KB
	}
	if config.ReadBufferSize == 0 {
		config.ReadBufferSize = 1024
	}
	if config.WriteBufferSize == 0 {
		config.WriteBufferSize = 1024
	}

	wsr := &WebSocketRouter{
		backends:       make([]backend.Backend, 0),
		config:         config,
		connections:    make(map[string]*WebSocketConnection),
		connectionPool: make(map[string][]*websocket.Conn),
		upgrader: websocket.Upgrader{
			ReadBufferSize:    config.ReadBufferSize,
			WriteBufferSize:   config.WriteBufferSize,
			EnableCompression: config.EnableCompression,
			CheckOrigin: func(r *http.Request) bool {
				if !config.OriginCheck {
					return true
				}
				origin := r.Header.Get("Origin")
				if origin == "" {
					return false
				}

				u, err := url.Parse(origin)
				if err != nil {
					return false
				}

				for _, allowedOrigin := range config.AllowedOrigins {
					if allowedOrigin == "*" || allowedOrigin == u.Host {
						return true
					}
				}
				return false
			},
		},
	}

	return wsr
}

func (wsr *WebSocketRouter) SetBackends(backends []backend.Backend) {
	wsr.mu.Lock()
	defer wsr.mu.Unlock()
	wsr.backends = backends
}

func (wsr *WebSocketRouter) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !wsr.config.Enabled {
		http.Error(w, "WebSocket support disabled", http.StatusServiceUnavailable)
		return
	}

	// Select backend for WebSocket connection
	backend := wsr.selectBackend()
	if backend == nil {
		http.Error(w, "No available backends for WebSocket", http.StatusServiceUnavailable)
		return
	}

	// Upgrade HTTP connection to WebSocket
	clientConn, err := wsr.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// Create WebSocket connection handler
	wsConn := &WebSocketConnection{
		conn:         clientConn,
		backend:      backend,
		isConnected:  true,
		lastActivity: time.Now(),
	}

	// Store connection
	connID := wsr.generateConnectionID()
	wsr.mu.Lock()
	wsr.connections[connID] = wsConn
	wsr.mu.Unlock()

	// Handle WebSocket connection in goroutine
	go wsr.handleWebSocketConnection(wsConn, connID)
}

func (wsr *WebSocketRouter) selectBackend() backend.Backend {
	wsr.mu.RLock()
	defer wsr.mu.RUnlock()

	for _, backend := range wsr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}

	return nil
}

func (wsr *WebSocketRouter) handleWebSocketConnection(wsConn *WebSocketConnection, connID string) {
	defer func() {
		wsr.cleanupConnection(connID)
	}()

	// Connect to backend WebSocket server
	backendConn, err := wsr.connectToBackend(wsConn.backend)
	if err != nil {
		return
	}
	wsConn.backendConn = backendConn

	// Start bidirectional message forwarding
	done := make(chan struct{})

	// Client to backend
	go wsr.forwardMessages(wsConn.conn, wsConn.backendConn, done)

	// Backend to client
	go wsr.forwardMessages(wsConn.backendConn, wsConn.conn, done)

	// Handle ping/pong
	go wsr.handlePingPong(wsConn)

	// Wait for connection to close
	<-done
}

func (wsr *WebSocketRouter) connectToBackend(backend backend.Backend) (*websocket.Conn, error) {
	backendURL := backend.GetURL()

	// Convert HTTP URL to WebSocket URL
	wsURL := url.URL{
		Scheme: "ws",
		Host:   backendURL.Host,
		Path:   "/ws", // Default WebSocket path
	}

	if backendURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Configure based on your security requirements
		},
	}

	conn, _, err := dialer.Dial(wsURL.String(), nil)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func (wsr *WebSocketRouter) forwardMessages(src, dst *websocket.Conn, done chan struct{}) {
	defer close(done)

	src.SetReadLimit(wsr.config.MaxMessageSize)
	src.SetReadDeadline(time.Now().Add(wsr.config.PongWait))

	for {
		messageType, message, err := src.ReadMessage()
		if err != nil {
			break
		}

		dst.SetWriteDeadline(time.Now().Add(wsr.config.WriteWait))
		if err := dst.WriteMessage(messageType, message); err != nil {
			break
		}
	}
}

func (wsr *WebSocketRouter) handlePingPong(wsConn *WebSocketConnection) {
	ticker := time.NewTicker(wsr.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			wsConn.mu.Lock()
			if !wsConn.isConnected {
				wsConn.mu.Unlock()
				return
			}

			wsConn.conn.SetWriteDeadline(time.Now().Add(wsr.config.WriteWait))
			if err := wsConn.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				wsConn.isConnected = false
				wsConn.mu.Unlock()
				return
			}
			wsConn.lastActivity = time.Now()
			wsConn.mu.Unlock()
		}
	}
}

func (wsr *WebSocketRouter) cleanupConnection(connID string) {
	wsr.mu.Lock()
	defer wsr.mu.Unlock()

	if wsConn, exists := wsr.connections[connID]; exists {
		wsConn.mu.Lock()
		wsConn.isConnected = false

		// Close connections
		if wsConn.conn != nil {
			wsConn.conn.Close()
		}
		if wsConn.backendConn != nil {
			wsConn.backendConn.Close()
		}

		wsConn.mu.Unlock()
		delete(wsr.connections, connID)
	}
}

func (wsr *WebSocketRouter) generateConnectionID() string {
	return fmt.Sprintf("ws_%d", time.Now().UnixNano())
}

func (wsr *WebSocketRouter) GetConnectionStats() WebSocketStats {
	wsr.mu.RLock()
	defer wsr.mu.RUnlock()

	stats := WebSocketStats{
		TotalConnections:     len(wsr.connections),
		ConnectionsByBackend: make(map[string]int),
	}

	for _, wsConn := range wsr.connections {
		backendURL := wsConn.backend.GetURL().String()
		stats.ConnectionsByBackend[backendURL]++
	}

	return stats
}

type WebSocketStats struct {
	TotalConnections     int            `json:"total_connections"`
	ConnectionsByBackend map[string]int `json:"connections_by_backend"`
}

// WebSocketLoadBalancer extends WebSocketRouter with load balancing features
type WebSocketLoadBalancer struct {
	*WebSocketRouter
	stickySessions bool
	sessionMap     map[string]string // Session ID -> Connection ID
	mu             sync.RWMutex
}

func NewWebSocketLoadBalancer(config WebSocketConfig, stickySessions bool) *WebSocketLoadBalancer {
	return &WebSocketLoadBalancer{
		WebSocketRouter: NewWebSocketRouter(config),
		stickySessions:  stickySessions,
		sessionMap:      make(map[string]string),
	}
}

func (wslb *WebSocketLoadBalancer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !wslb.config.Enabled {
		http.Error(w, "WebSocket support disabled", http.StatusServiceUnavailable)
		return
	}

	var backend backend.Backend
	var connID string

	// Check for sticky session
	if wslb.stickySessions {
		sessionID := wslb.extractSessionID(r)
		if sessionID != "" {
			if existingConnID := wslb.getSessionConnection(sessionID); existingConnID != "" {
				// Reuse existing connection if available
				wslb.mu.RLock()
				if wsConn, exists := wslb.connections[existingConnID]; exists && wsConn.isConnected {
					backend = wsConn.backend
					connID = existingConnID
				}
				wslb.mu.RUnlock()
			}
		}
	}

	// Select new backend if needed
	if backend == nil {
		backend = wslb.selectBackend()
		if backend == nil {
			http.Error(w, "No available backends for WebSocket", http.StatusServiceUnavailable)
			return
		}
		connID = wslb.generateConnectionID()
	}

	// Upgrade HTTP connection to WebSocket
	clientConn, err := wslb.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// Create WebSocket connection handler
	wsConn := &WebSocketConnection{
		conn:         clientConn,
		backend:      backend,
		isConnected:  true,
		lastActivity: time.Now(),
	}

	// Store connection
	wslb.mu.Lock()
	wslb.connections[connID] = wsConn

	// Store session mapping
	if wslb.stickySessions {
		sessionID := wslb.extractSessionID(r)
		if sessionID != "" {
			wslb.sessionMap[sessionID] = connID
		}
	}
	wslb.mu.Unlock()

	// Handle WebSocket connection in goroutine
	go wslb.handleWebSocketConnection(wsConn, connID)
}

func (wslb *WebSocketLoadBalancer) extractSessionID(r *http.Request) string {
	// Try to get session ID from various sources
	if sessionID := r.Header.Get("X-Session-ID"); sessionID != "" {
		return sessionID
	}
	if sessionID := r.URL.Query().Get("session_id"); sessionID != "" {
		return sessionID
	}
	if cookie, err := r.Cookie("session_id"); err == nil {
		return cookie.Value
	}
	return ""
}

func (wslb *WebSocketLoadBalancer) getSessionConnection(sessionID string) string {
	wslb.mu.RLock()
	defer wslb.mu.RUnlock()
	return wslb.sessionMap[sessionID]
}

func (wslb *WebSocketLoadBalancer) cleanupConnection(connID string) {
	wslb.mu.Lock()
	defer wslb.mu.Unlock()

	// Remove from session map
	for sessionID, sessionConnID := range wslb.sessionMap {
		if sessionConnID == connID {
			delete(wslb.sessionMap, sessionID)
			break
		}
	}

	// Call parent cleanup
	wslb.WebSocketRouter.cleanupConnection(connID)
}

// WebSocketProxy for proxying WebSocket connections to different protocols
type WebSocketProxy struct {
	protocolMap map[string]string // Protocol -> Backend URL pattern
	mu          sync.RWMutex
}

func NewWebSocketProxy() *WebSocketProxy {
	return &WebSocketProxy{
		protocolMap: make(map[string]string),
	}
}

func (wsp *WebSocketProxy) AddProtocol(protocol string, backendPattern string) {
	wsp.mu.Lock()
	defer wsp.mu.Unlock()
	wsp.protocolMap[protocol] = backendPattern
}

func (wsp *WebSocketProxy) GetBackendForProtocol(protocol string) string {
	wsp.mu.RLock()
	defer wsp.mu.RUnlock()
	return wsp.protocolMap[protocol]
}

func (wsp *WebSocketProxy) DetectProtocol(r *http.Request) string {
	// Detect protocol from headers, path, or query parameters
	if proto := r.Header.Get("X-WebSocket-Protocol"); proto != "" {
		return proto
	}
	if proto := r.URL.Query().Get("protocol"); proto != "" {
		return proto
	}

	// Detect from path
	path := r.URL.Path
	if strings.HasPrefix(path, "/chat/") {
		return "chat"
	}
	if strings.HasPrefix(path, "/notification/") {
		return "notification"
	}
	if strings.HasPrefix(path, "/stream/") {
		return "stream"
	}

	return "default"
}
