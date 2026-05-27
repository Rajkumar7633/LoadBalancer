package advanced_routing

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"loadbalancer/internal/backend"
)

type GRPCConfig struct {
	Enabled          bool     `json:"enabled"`
	Services         []string `json:"services"` // gRPC services to route
	MaxRecvMsgSize   int      `json:"max_recv_msg_size"`
	MaxSendMsgSize   int      `json:"max_send_msg_size"`
	Compression      string   `json:"compression"` // "gzip", "none"
	EnableReflection bool     `json:"enable_reflection"`
	EnableHealth     bool     `json:"enable_health"`
	EnableTracing    bool     `json:"enable_tracing"`
}

type GRPCRouter struct {
	backends         []backend.Backend
	config           GRPCConfig
	grpcServers      map[string]*grpc.Server // Service -> gRPC server
	serviceDiscovery *ServiceDiscovery
	mu               sync.RWMutex
}

type GRPCBackendInfo struct {
	Backend    backend.Backend
	Service    string
	Method     string
	FullMethod string // "/service/method"
	Metadata   metadata.MD
}

func NewGRPCRouter(config GRPCConfig) *GRPCRouter {
	if config.MaxRecvMsgSize == 0 {
		config.MaxRecvMsgSize = 4 * 1024 * 1024 // 4MB
	}
	if config.MaxSendMsgSize == 0 {
		config.MaxSendMsgSize = 4 * 1024 * 1024 // 4MB
	}
	if config.Compression == "" {
		config.Compression = "gzip"
	}

	return &GRPCRouter{
		backends:         make([]backend.Backend, 0),
		config:           config,
		grpcServers:      make(map[string]*grpc.Server),
		serviceDiscovery: NewServiceDiscovery(),
	}
}

func (gr *GRPCRouter) SetBackends(backends []backend.Backend) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	gr.backends = backends

	// Update service discovery with new backends
	if gr.serviceDiscovery != nil {
		// Map backends to services based on configuration
		for _, b := range backends {
			// In a real implementation, this would be based on backend metadata
			// For now, assume all backends support all configured services
			for _, service := range gr.config.Services {
				gr.serviceDiscovery.RegisterService(service, []backend.Backend{b})
			}
		}
	}
}

func (gr *GRPCRouter) RouteGRPC(ctx context.Context, fullMethod string, md metadata.MD) backend.Backend {
	if !gr.config.Enabled {
		return gr.selectBackend()
	}

	// Extract service and method from fullMethod
	service, method := parseGRPCMethod(fullMethod)

	// Check if service is in our service list
	if !gr.isServiceAllowed(service) {
		return nil
	}

	// Route based on service/method
	backend := gr.routeByService(service, method, md)
	if backend == nil {
		backend = gr.selectBackend()
	}

	return backend
}

func (gr *GRPCRouter) isServiceAllowed(service string) bool {
	if len(gr.config.Services) == 0 {
		return true // Allow all services if no specific list
	}

	for _, allowedService := range gr.config.Services {
		if allowedService == service || strings.HasSuffix(allowedService, "/*") && strings.HasPrefix(service, strings.TrimSuffix(allowedService, "/*")) {
			return true
		}
	}

	return false
}

func (gr *GRPCRouter) routeByService(service string, method string, md metadata.MD) backend.Backend {
	gr.mu.RLock()
	defer gr.mu.RUnlock()

	// Simple routing based on service name
	// In a real implementation, this could use more sophisticated routing rules
	for _, backend := range gr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			// Check if backend supports this service (could be stored in backend metadata)
			if gr.backendSupportsService(backend, service) {
				return backend
			}
		}
	}

	return nil
}

func (gr *GRPCRouter) backendSupportsService(backend backend.Backend, service string) bool {
	// This would check backend metadata or capabilities
	// For now, assume all backends support all services
	return true
}

func (gr *GRPCRouter) selectBackend() backend.Backend {
	gr.mu.RLock()
	defer gr.mu.RUnlock()

	for _, backend := range gr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}

	return nil
}

func parseGRPCMethod(fullMethod string) (service, method string) {
	parts := strings.Split(fullMethod, "/")
	if len(parts) >= 3 {
		return parts[1], parts[2]
	}
	return "", ""
}

// HTTP2Handler handles HTTP/2 connections
type HTTP2Handler struct {
	grpcRouter *GRPCRouter
	next       http.Handler
}

func NewHTTP2Handler(grpcRouter *GRPCRouter, next http.Handler) *HTTP2Handler {
	return &HTTP2Handler{
		grpcRouter: grpcRouter,
		next:       next,
	}
}

func (h *HTTP2Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if this is an HTTP/2 request
	if r.ProtoMajor == 2 {
		h.handleHTTP2(w, r)
		return
	}

	// Fall back to regular HTTP handler
	h.next.ServeHTTP(w, r)
}

func (h *HTTP2Handler) handleHTTP2(w http.ResponseWriter, r *http.Request) {
	// Check if this is a gRPC request
	if isGRPCRequest(r) {
		h.handleGRPC(w, r)
		return
	}

	// Handle regular HTTP/2 request
	h.next.ServeHTTP(w, r)
}

func isGRPCRequest(r *http.Request) bool {
	return r.Header.Get("Content-Type") == "application/grpc"
}

func (h *HTTP2Handler) handleGRPC(w http.ResponseWriter, r *http.Request) {
	// Extract gRPC method from headers
	method := r.Header.Get("Grpc-Method")
	if method == "" {
		http.Error(w, "Missing gRPC method", http.StatusBadRequest)
		return
	}

	// Create metadata from headers
	md := make(metadata.MD)
	for key, values := range r.Header {
		// Convert HTTP headers to gRPC metadata format
		md[key] = values
	}

	// Route to appropriate backend
	backend := h.grpcRouter.RouteGRPC(r.Context(), method, md)
	if backend == nil {
		http.Error(w, "No available backend for gRPC service", http.StatusServiceUnavailable)
		return
	}

	// Forward gRPC request to backend
	h.forwardGRPCRequest(w, r, backend)
}

func (h *HTTP2Handler) forwardGRPCRequest(w http.ResponseWriter, r *http.Request, backend backend.Backend) {
	// This would implement gRPC request forwarding
	// For now, just return a placeholder response
	w.Header().Set("Content-Type", "application/grpc")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("gRPC response placeholder"))
}

// GRPCHealthChecker implements gRPC health checking
type GRPCHealthChecker struct {
	healthServer *health.Server
}

func NewGRPCHealthChecker() *GRPCHealthChecker {
	return &GRPCHealthChecker{
		healthServer: health.NewServer(),
	}
}

func (ghc *GRPCHealthChecker) SetServiceStatus(service string, status grpc_health_v1.HealthCheckResponse_ServingStatus) {
	ghc.healthServer.SetServingStatus(service, status)
}

func (ghc *GRPCHealthChecker) Register(server *grpc.Server) {
	grpc_health_v1.RegisterHealthServer(server, ghc.healthServer)
}

// GRPCInterceptor for load balancing
type GRPCLoadBalancerInterceptor struct {
	router *GRPCRouter
}

func NewGRPCLoadBalancerInterceptor(router *GRPCRouter) *GRPCLoadBalancerInterceptor {
	return &GRPCLoadBalancerInterceptor{
		router: router,
	}
}

func (gli *GRPCLoadBalancerInterceptor) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Extract method from info
		fullMethod := info.FullMethod

		// Extract metadata from context
		md, _ := metadata.FromIncomingContext(ctx)

		// Route to appropriate backend
		backend := gli.router.RouteGRPC(ctx, fullMethod, md)
		if backend == nil {
			return nil, fmt.Errorf("no available backend for service %s", fullMethod)
		}

		// Add backend info to context
		ctx = context.WithValue(ctx, "backend", backend)

		// Call the handler
		return handler(ctx, req)
	}
}

func (gli *GRPCLoadBalancerInterceptor) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Extract method from info
		fullMethod := info.FullMethod

		// Extract metadata from context
		ctx := ss.Context()
		md, _ := metadata.FromIncomingContext(ctx)

		// Route to appropriate backend
		backend := gli.router.RouteGRPC(ctx, fullMethod, md)
		if backend == nil {
			return fmt.Errorf("no available backend for service %s", fullMethod)
		}

		// Add backend info to context
		ctx = context.WithValue(ctx, "backend", backend)

		// Wrap the stream with new context
		wrappedStream := &contextServerStream{
			ServerStream: ss,
			ctx:          ctx,
		}

		// Call the handler
		return handler(srv, wrappedStream)
	}
}

// contextServerStream wraps grpc.ServerStream to replace context
type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (css *contextServerStream) Context() context.Context {
	return css.ctx
}

// HTTP2Server creates an HTTP/2 server with gRPC support
type HTTP2Server struct {
	server      *http.Server
	grpcRouter  *GRPCRouter
	httpHandler http.Handler
}

func NewHTTP2Server(addr string, grpcRouter *GRPCRouter, httpHandler http.Handler) *HTTP2Server {
	http2Handler := NewHTTP2Handler(grpcRouter, httpHandler)

	return &HTTP2Server{
		server: &http.Server{
			Addr:    addr,
			Handler: http2Handler,
		},
		grpcRouter:  grpcRouter,
		httpHandler: httpHandler,
	}
}

func (h2s *HTTP2Server) Start(tlsConfig *tls.Config) error {
	if tlsConfig != nil {
		h2s.server.TLSConfig = tlsConfig
		return h2s.server.ListenAndServeTLS("", "")
	}
	return h2s.server.ListenAndServe()
}

func (h2s *HTTP2Server) Stop() error {
	return h2s.server.Close()
}

func (h2s *HTTP2Server) CreateGRPCServer() *grpc.Server {
	interceptor := NewGRPCLoadBalancerInterceptor(h2s.grpcRouter)
	opts := []grpc.ServerOption{
		grpc.UnaryInterceptor(interceptor.UnaryInterceptor()),
		grpc.StreamInterceptor(interceptor.StreamInterceptor()),
	}

	return grpc.NewServer(opts...)
}

// ServiceDiscovery for gRPC services
type ServiceDiscovery struct {
	services map[string][]backend.Backend
	mu       sync.RWMutex
}

func NewServiceDiscovery() *ServiceDiscovery {
	return &ServiceDiscovery{
		services: make(map[string][]backend.Backend),
	}
}

func (sd *ServiceDiscovery) RegisterService(serviceName string, backends []backend.Backend) {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	sd.services[serviceName] = backends
}

func (sd *ServiceDiscovery) GetBackendsForService(serviceName string) []backend.Backend {
	sd.mu.RLock()
	defer sd.mu.RUnlock()
	return sd.services[serviceName]
}

func (sd *ServiceDiscovery) RemoveService(serviceName string) {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	delete(sd.services, serviceName)
}

func (sd *ServiceDiscovery) ListServices() []string {
	sd.mu.RLock()
	defer sd.mu.RUnlock()

	services := make([]string, 0, len(sd.services))
	for service := range sd.services {
		services = append(services, service)
	}
	return services
}
