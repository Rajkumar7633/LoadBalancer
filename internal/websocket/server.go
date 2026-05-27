package websocket

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// WebSocketMessage represents a message sent to clients
type WebSocketMessage struct {
	Timestamp      string                 `json:"timestamp"`
	Metrics        map[string]interface{} `json:"metrics,omitempty"`
	Backends       []BackendStatus        `json:"backends,omitempty"`
	SecurityEvents []SecurityEvent        `json:"security_events,omitempty"`
	Autoscaling    AutoscalingStatus      `json:"autoscaling,omitempty"`
	TopEndpoints   []EndpointStats        `json:"top_endpoints,omitempty"`
}

// BackendStatus represents backend status information
type BackendStatus struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Status       string `json:"status"` // healthy, unhealthy, draining
	Connections  int    `json:"connections"`
	ResponseTime int    `json:"response_time"`
	RequestCount int64  `json:"request_count"`
	ErrorCount   int64  `json:"error_count"`
}

// SecurityEvent represents a security event
type SecurityEvent struct {
	Type      string    `json:"type"` // WAF_BLOCK, RATE_LIMIT, AUTH_FAILURE, IP_BLOCKED
	Message   string    `json:"message"`
	Severity  string    `json:"severity"` // high, medium, low
	Timestamp time.Time `json:"timestamp"`
	ClientIP  string    `json:"client_ip"`
	UserAgent string    `json:"user_agent"`
}

// AutoscalingStatus represents auto-scaling status
type AutoscalingStatus struct {
	CurrentScale      int       `json:"current_scale"`
	MinScale          int       `json:"min_scale"`
	MaxScale          int       `json:"max_scale"`
	CPUUsage          float64   `json:"cpu_usage"`
	MemoryUsage       float64   `json:"memory_usage"`
	RequestRate       float64   `json:"request_rate"`
	ResponseTime      float64   `json:"response_time"`
	LastEvent         string    `json:"last_event"`
	LastEventTime     time.Time `json:"last_event_time"`
	ScaleUpCooldown   bool      `json:"scale_up_cooldown"`
	ScaleDownCooldown bool      `json:"scale_down_cooldown"`
}

// EndpointStats represents endpoint statistics
type EndpointStats struct {
	Path            string  `json:"path"`
	Requests        int64   `json:"requests"`
	AvgResponseTime int     `json:"avg_response_time"`
	ErrorRate       float64 `json:"error_rate"`
	LastMinuteRPS   int     `json:"last_minute_rps"`
}

// WebSocketServer handles WebSocket connections
type WebSocketServer struct {
	upgrader websocket.Upgrader
	clients  map[*websocket.Conn]bool
	mutex    sync.RWMutex
	logger   *zap.Logger
	metrics  *MetricsCollector
	done     chan struct{}
}

// MetricsCollector collects real metrics from the load balancer
type MetricsCollector struct {
	requestsPerSecond int64
	activeConnections int64
	avgResponseTime   int64
	errorRate         float64
	cpuUsage          float64
	memoryUsage       float64
	backendStatus     []BackendStatus
	securityEvents    []SecurityEvent
	topEndpoints      []EndpointStats
	autoscalingStatus AutoscalingStatus
	mutex             sync.RWMutex
}

// NewWebSocketServer creates a new WebSocket server
func NewWebSocketServer(logger *zap.Logger) *WebSocketServer {
	return &WebSocketServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
		clients: make(map[*websocket.Conn]bool),
		logger:  logger,
		metrics: NewMetricsCollector(),
		done:    make(chan struct{}),
	}
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

// Start starts the WebSocket server
func (ws *WebSocketServer) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ws.handleWebSocket)
	mux.HandleFunc("/", ws.serveDashboard)

	ws.logger.Info("Starting WebSocket server", zap.String("addr", addr))

	go ws.broadcastLoop()

	return http.ListenAndServe(addr, mux)
}

// handleWebSocket handles WebSocket connections
func (ws *WebSocketServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.upgrader.Upgrade(w, r, nil)
	if err != nil {
		ws.logger.Error("Failed to upgrade WebSocket", zap.Error(err))
		return
	}
	defer conn.Close()

	ws.mutex.Lock()
	ws.clients[conn] = true
	ws.mutex.Unlock()

	ws.logger.Info("WebSocket client connected")

	// Send initial data
	ws.sendInitialData(conn)

	// Keep connection alive
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			ws.logger.Info("WebSocket client disconnected", zap.Error(err))
			break
		}
	}

	ws.mutex.Lock()
	delete(ws.clients, conn)
	ws.mutex.Unlock()
}

// serveDashboard serves the dashboard HTML
func (ws *WebSocketServer) serveDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/dashboard.html")
}

// sendInitialData sends initial data to a new client
func (ws *WebSocketServer) sendInitialData(conn *websocket.Conn) {
	message := ws.generateMessage()
	if err := conn.WriteJSON(message); err != nil {
		ws.logger.Error("Failed to send initial data", zap.Error(err))
	}
}

// broadcastLoop broadcasts data to all connected clients
func (ws *WebSocketServer) broadcastLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			message := ws.generateMessage()
			ws.broadcast(message)
		case <-ws.done:
			return
		}
	}
}

// generateMessage generates a message with current metrics
func (ws *WebSocketServer) generateMessage() WebSocketMessage {
	ws.metrics.mutex.RLock()
	defer ws.metrics.mutex.RUnlock()

	return WebSocketMessage{
		Timestamp: time.Now().Format(time.RFC3339),
		Metrics: map[string]interface{}{
			"requests_per_second": ws.metrics.requestsPerSecond,
			"active_connections":  ws.metrics.activeConnections,
			"avg_response_time":   ws.metrics.avgResponseTime,
			"error_rate":          ws.metrics.errorRate,
			"cpu_usage":           ws.metrics.cpuUsage,
			"memory_usage":        ws.metrics.memoryUsage,
		},
		Backends:       ws.generateBackendStatus(),
		SecurityEvents: ws.generateSecurityEvents(),
		Autoscaling:    ws.generateAutoscalingStatus(),
		TopEndpoints:   ws.generateTopEndpoints(),
	}
}

// generateBackendStatus generates real backend status information
func (ws *WebSocketServer) generateBackendStatus() []BackendStatus {
	ws.metrics.mutex.RLock()
	defer ws.metrics.mutex.RUnlock()

	// Return real backend data from metrics collector
	// This will be populated by the actual load balancer
	return ws.metrics.backendStatus
}

// generateSecurityEvents generates real security events
func (ws *WebSocketServer) generateSecurityEvents() []SecurityEvent {
	ws.metrics.mutex.RLock()
	defer ws.metrics.mutex.RUnlock()

	// Return real security events from metrics collector
	// This will be populated by the actual security manager
	return ws.metrics.securityEvents
}

// generateAutoscalingStatus generates real auto-scaling status
func (ws *WebSocketServer) generateAutoscalingStatus() AutoscalingStatus {
	ws.metrics.mutex.RLock()
	defer ws.metrics.mutex.RUnlock()

	// Return real autoscaling status from metrics collector
	// This will be populated by the actual autoscaler
	return ws.metrics.autoscalingStatus
}

// generateTopEndpoints generates real top endpoints statistics
func (ws *WebSocketServer) generateTopEndpoints() []EndpointStats {
	ws.metrics.mutex.RLock()
	defer ws.metrics.mutex.RUnlock()

	// Return real endpoint statistics from metrics collector
	// This will be populated by the actual load balancer
	return ws.metrics.topEndpoints
}

// broadcast sends a message to all connected clients
func (ws *WebSocketServer) broadcast(message WebSocketMessage) {
	ws.mutex.RLock()
	defer ws.mutex.RUnlock()

	for conn := range ws.clients {
		if err := conn.WriteJSON(message); err != nil {
			ws.logger.Error("Failed to send message to client", zap.Error(err))
			conn.Close()
			delete(ws.clients, conn)
		}
	}
}

// UpdateMetrics updates the performance metrics (to be called by the load balancer)
func (ws *WebSocketServer) UpdateMetrics(requestsPerSecond, activeConnections, avgResponseTime int64, errorRate, cpuUsage, memoryUsage float64) {
	ws.metrics.mutex.Lock()
	defer ws.metrics.mutex.Unlock()

	ws.metrics.requestsPerSecond = requestsPerSecond
	ws.metrics.activeConnections = activeConnections
	ws.metrics.avgResponseTime = avgResponseTime
	ws.metrics.errorRate = errorRate
	ws.metrics.cpuUsage = cpuUsage
	ws.metrics.memoryUsage = memoryUsage
}

// UpdateBackendStatus updates backend status information
func (ws *WebSocketServer) UpdateBackendStatus(backends []BackendStatus) {
	ws.metrics.mutex.Lock()
	defer ws.metrics.mutex.Unlock()
	ws.metrics.backendStatus = backends
}

// UpdateSecurityEvents updates security events
func (ws *WebSocketServer) UpdateSecurityEvents(events []SecurityEvent) {
	ws.metrics.mutex.Lock()
	defer ws.metrics.mutex.Unlock()
	ws.metrics.securityEvents = events
}

// UpdateTopEndpoints updates top endpoints statistics
func (ws *WebSocketServer) UpdateTopEndpoints(endpoints []EndpointStats) {
	ws.metrics.mutex.Lock()
	defer ws.metrics.mutex.Unlock()
	ws.metrics.topEndpoints = endpoints
}

// UpdateAutoscalingStatus updates auto-scaling status
func (ws *WebSocketServer) UpdateAutoscalingStatus(status AutoscalingStatus) {
	ws.metrics.mutex.Lock()
	defer ws.metrics.mutex.Unlock()
	ws.metrics.autoscalingStatus = status
}

// AddSecurityEvent adds a new security event
func (ws *WebSocketServer) AddSecurityEvent(event SecurityEvent) {
	ws.metrics.mutex.Lock()
	defer ws.metrics.mutex.Unlock()

	// Keep only last 100 events
	if len(ws.metrics.securityEvents) >= 100 {
		ws.metrics.securityEvents = ws.metrics.securityEvents[1:]
	}
	ws.metrics.securityEvents = append(ws.metrics.securityEvents, event)
}

// Stop stops the WebSocket server
func (ws *WebSocketServer) Stop() {
	close(ws.done)

	ws.mutex.Lock()
	for conn := range ws.clients {
		conn.Close()
	}
	ws.mutex.Unlock()

	ws.logger.Info("WebSocket server stopped")
}

// GetClientCount returns the number of connected clients
func (ws *WebSocketServer) GetClientCount() int {
	ws.mutex.RLock()
	defer ws.mutex.RUnlock()
	return len(ws.clients)
}
