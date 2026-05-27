package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Config struct {
	Port           int               `json:"port" yaml:"port"`
	RequestTimeout int               `json:"request_timeout" yaml:"request_timeout"`
	TLS            TLSConfig         `json:"tls" yaml:"tls"`
	Backends       []BackendConfig   `json:"backends" yaml:"backends"`
	HealthCheck    HealthCheckConfig `json:"health_check" yaml:"health_check"`
	RateLimit      RateLimitConfig   `json:"rate_limit" yaml:"rate_limit"`
	Logging        LoggingConfig     `json:"logging" yaml:"logging"`
	Metrics        MetricsConfig     `json:"metrics" yaml:"metrics"`
	Routing        RoutingConfig     `json:"routing" yaml:"routing"`
	Pool           PoolConfig        `json:"pool" yaml:"pool"`
	// Tier 2 Advanced Routing Features
	ContentBasedRouting ContentBasedRoutingConfig `json:"content_based_routing" yaml:"content_based_routing"`
	ConsistentHash      ConsistentHashConfig      `json:"consistent_hash" yaml:"consistent_hash"`
	IPHash              IPHashConfig              `json:"ip_hash" yaml:"ip_hash"`
	CircuitBreaker      CircuitBreakerConfig      `json:"circuit_breaker" yaml:"circuit_breaker"`
	Retry               RetryConfig               `json:"retry" yaml:"retry"`
	RequestMirroring    RequestMirroringConfig    `json:"request_mirroring" yaml:"request_mirroring"`
	SlowStart           SlowStartConfig           `json:"slow_start" yaml:"slow_start"`
	PriorityQueue       PriorityQueueConfig       `json:"priority_queue" yaml:"priority_queue"`
	MTLS                MTLSConfig                `json:"mtls" yaml:"mtls"`
	GRPC                GRPCConfig                `json:"grpc" yaml:"grpc"`
	WebSocket           WebSocketConfig           `json:"websocket" yaml:"websocket"`
	ResponseCache       ResponseCacheConfig       `json:"response_cache" yaml:"response_cache"`
	// Tier 3 Advanced Features
	GeoRouting         GeoRoutingConfig     `json:"geo_routing" yaml:"geo_routing"`
	AdaptiveRouting    AdaptiveConfig       `json:"adaptive_routing" yaml:"adaptive_routing"`
	Transformation     TransformationConfig `json:"transformation" yaml:"transformation"`
	BlueGreen          BlueGreenConfig      `json:"blue_green" yaml:"blue_green"`
	APIGateway         APIGatewayConfig     `json:"api_gateway" yaml:"api_gateway"`
	DistributedTracing TracingConfig        `json:"distributed_tracing" yaml:"distributed_tracing"`
}

type TLSConfig struct {
	CertFile string `json:"cert_file" yaml:"cert_file"`
	KeyFile  string `json:"key_file" yaml:"key_file"`
}

type BackendConfig struct {
	URL             string        `json:"url" yaml:"url"`
	Weight          int           `json:"weight" yaml:"weight"`
	MaxConnections  int           `json:"max_connections" yaml:"max_connections"`
	HealthCheckPath string        `json:"health_check_path" yaml:"health_check_path"`
	Timeout         time.Duration `json:"timeout" yaml:"timeout"`
	Draining        bool          `json:"draining" yaml:"draining"`
}

type HealthCheckConfig struct {
	Interval         time.Duration `json:"interval" yaml:"interval"`
	Timeout          time.Duration `json:"timeout" yaml:"timeout"`
	FailureThreshold int           `json:"failure_threshold" yaml:"failure_threshold"`
	SuccessThreshold int           `json:"success_threshold" yaml:"success_threshold"`
	Path             string        `json:"path" yaml:"path"`
}

type RateLimitConfig struct {
	RequestsPerSecond int           `json:"requests_per_second" yaml:"requests_per_second"`
	BurstSize         int           `json:"burst_size" yaml:"burst_size"`
	Window            time.Duration `json:"window" yaml:"window"`
}

type LoggingConfig struct {
	Level      string `json:"level" yaml:"level"`
	Format     string `json:"format" yaml:"format"`
	Output     string `json:"output" yaml:"output"`
	Structured bool   `json:"structured" yaml:"structured"`
}

type MetricsConfig struct {
	Port int    `json:"port" yaml:"port"`
	Path string `json:"path" yaml:"path"`
}

type RoutingConfig struct {
	Algorithm        string `json:"algorithm" yaml:"algorithm"` // round_robin, weighted, least_connections
	StickySessions   bool   `json:"sticky_sessions" yaml:"sticky_sessions"`
	CookieName       string `json:"cookie_name" yaml:"cookie_name"`
	AdaptiveRouting  bool   `json:"adaptive_routing" yaml:"adaptive_routing"`
	CircuitBreaker   bool   `json:"circuit_breaker" yaml:"circuit_breaker"`
	FailureThreshold int    `json:"failure_threshold" yaml:"failure_threshold"`
}

type PoolConfig struct {
	MaxIdleConns        int           `json:"max_idle_conns" yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int           `json:"max_idle_conns_per_host" yaml:"max_idle_conns_per_host"`
	IdleConnTimeout     time.Duration `json:"idle_conn_timeout" yaml:"idle_conn_timeout"`
}

// Tier 2 Advanced Routing Configuration Structures

type ContentBasedRoutingConfig struct {
	Enabled bool                     `json:"enabled" yaml:"enabled"`
	Rules   []ContentBasedRuleConfig `json:"rules" yaml:"rules"`
}

type ContentBasedRuleConfig struct {
	Name        string               `json:"name" yaml:"name"`
	Priority    int                  `json:"priority" yaml:"priority"`
	Condition   RouteConditionConfig `json:"condition" yaml:"condition"`
	BackendRefs []string             `json:"backend_refs" yaml:"backend_refs"`
}

type RouteConditionConfig struct {
	PathMatch   *PathMatchConfig   `json:"path_match,omitempty" yaml:"path_match,omitempty"`
	HeaderMatch *HeaderMatchConfig `json:"header_match,omitempty" yaml:"header_match,omitempty"`
	QueryMatch  *QueryMatchConfig  `json:"query_match,omitempty" yaml:"query_match,omitempty"`
	MethodMatch []string           `json:"method_match,omitempty" yaml:"method_match,omitempty"`
}

type PathMatchConfig struct {
	Type   string   `json:"type" yaml:"type"` // "exact", "prefix", "regex", "suffix"
	Values []string `json:"values" yaml:"values"`
}

type HeaderMatchConfig struct {
	Name   string   `json:"name" yaml:"name"`
	Type   string   `json:"type" yaml:"type"` // "exact", "regex", "contains"
	Values []string `json:"values" yaml:"values"`
}

type QueryMatchConfig struct {
	Name   string   `json:"name" yaml:"name"`
	Type   string   `json:"type" yaml:"type"` // "exact", "regex", "contains"
	Values []string `json:"values" yaml:"values"`
}

type ConsistentHashConfig struct {
	Enabled  bool   `json:"enabled" yaml:"enabled"`
	Replicas int    `json:"replicas" yaml:"replicas"`
	KeyType  string `json:"key_type" yaml:"key_type"` // "user_id", "session_id", "api_key", "jwt"
}

type IPHashConfig struct {
	Enabled  bool `json:"enabled" yaml:"enabled"`
	Weighted bool `json:"weighted" yaml:"weighted"`
}

type CircuitBreakerConfig struct {
	Enabled               bool          `json:"enabled" yaml:"enabled"`
	FailureThreshold      int           `json:"failure_threshold" yaml:"failure_threshold"`
	SuccessThreshold      int           `json:"success_threshold" yaml:"success_threshold"`
	TimeoutThreshold      time.Duration `json:"timeout_threshold" yaml:"timeout_threshold"`
	RecoveryTimeout       time.Duration `json:"recovery_timeout" yaml:"recovery_timeout"`
	MaxHalfOpenRequests   int           `json:"max_half_open_requests" yaml:"max_half_open_requests"`
	SlowCallThreshold     time.Duration `json:"slow_call_threshold" yaml:"slow_call_threshold"`
	SlowCallRateThreshold float64       `json:"slow_call_rate_threshold" yaml:"slow_call_rate_threshold"`
}

type RetryConfig struct {
	Enabled          bool          `json:"enabled" yaml:"enabled"`
	MaxRetries       int           `json:"max_retries" yaml:"max_retries"`
	InitialDelay     time.Duration `json:"initial_delay" yaml:"initial_delay"`
	MaxDelay         time.Duration `json:"max_delay" yaml:"max_delay"`
	Multiplier       float64       `json:"multiplier" yaml:"multiplier"`
	Jitter           bool          `json:"jitter" yaml:"jitter"`
	RetryableStatus  []int         `json:"retryable_status" yaml:"retryable_status"`
	RetryableMethods []string      `json:"retryable_methods" yaml:"retryable_methods"`
}

type RequestMirroringConfig struct {
	Enabled        bool          `json:"enabled" yaml:"enabled"`
	Percentage     float64       `json:"percentage" yaml:"percentage"`
	MirrorBackends []string      `json:"mirror_backends" yaml:"mirror_backends"`
	Timeout        time.Duration `json:"timeout" yaml:"timeout"`
	HeadersToCopy  []string      `json:"headers_to_copy" yaml:"headers_to_copy"`
	Async          bool          `json:"async" yaml:"async"`
	BufferSize     int           `json:"buffer_size" yaml:"buffer_size"`
}

type SlowStartConfig struct {
	Enabled         bool          `json:"enabled" yaml:"enabled"`
	Duration        time.Duration `json:"duration" yaml:"duration"`
	InitialWeight   float64       `json:"initial_weight" yaml:"initial_weight"`
	StepInterval    time.Duration `json:"step_interval" yaml:"step_interval"`
	WeightIncrement float64       `json:"weight_increment" yaml:"weight_increment"`
}

type PriorityQueueConfig struct {
	Enabled         bool               `json:"enabled" yaml:"enabled"`
	QueueSize       int                `json:"queue_size" yaml:"queue_size"`
	ExpiryTime      time.Duration      `json:"expiry_time" yaml:"expiry_time"`
	PriorityHeaders map[string]string  `json:"priority_headers" yaml:"priority_headers"`
	PriorityPaths   map[string]string  `json:"priority_paths" yaml:"priority_paths"`
	DefaultPriority string             `json:"default_priority" yaml:"default_priority"`
	LoadShedding    LoadSheddingConfig `json:"load_shedding" yaml:"load_shedding"`
}

type LoadSheddingConfig struct {
	Enabled        bool    `json:"enabled" yaml:"enabled"`
	Threshold      float64 `json:"threshold" yaml:"threshold"`
	ShedPriority   string  `json:"shed_priority" yaml:"shed_priority"`
	ShedPercentage float64 `json:"shed_percentage" yaml:"shed_percentage"`
}

type MTLSConfig struct {
	Enabled        bool     `json:"enabled" yaml:"enabled"`
	CACertFile     string   `json:"ca_cert_file" yaml:"ca_cert_file"`
	ClientCertFile string   `json:"client_cert_file" yaml:"client_cert_file"`
	ClientKeyFile  string   `json:"client_key_file" yaml:"client_key_file"`
	ServerCertFile string   `json:"server_cert_file" yaml:"server_cert_file"`
	ServerKeyFile  string   `json:"server_key_file" yaml:"server_key_file"`
	SkipVerify     bool     `json:"skip_verify" yaml:"skip_verify"`
	ClientAuth     string   `json:"client_auth" yaml:"client_auth"`
	MinVersion     uint16   `json:"min_version" yaml:"min_version"`
	CipherSuites   []uint16 `json:"cipher_suites" yaml:"cipher_suites"`
}

type GRPCConfig struct {
	Enabled          bool     `json:"enabled" yaml:"enabled"`
	Services         []string `json:"services" yaml:"services"`
	MaxRecvMsgSize   int      `json:"max_recv_msg_size" yaml:"max_recv_msg_size"`
	MaxSendMsgSize   int      `json:"max_send_msg_size" yaml:"max_send_msg_size"`
	Compression      string   `json:"compression" yaml:"compression"`
	EnableReflection bool     `json:"enable_reflection" yaml:"enable_reflection"`
	EnableHealth     bool     `json:"enable_health" yaml:"enable_health"`
	EnableTracing    bool     `json:"enable_tracing" yaml:"enable_tracing"`
}

type WebSocketConfig struct {
	Enabled           bool          `json:"enabled" yaml:"enabled"`
	OriginCheck       bool          `json:"origin_check" yaml:"origin_check"`
	AllowedOrigins    []string      `json:"allowed_origins" yaml:"allowed_origins"`
	PingInterval      time.Duration `json:"ping_interval" yaml:"ping_interval"`
	PongWait          time.Duration `json:"pong_wait" yaml:"pong_wait"`
	WriteWait         time.Duration `json:"write_wait" yaml:"write_wait"`
	MaxMessageSize    int64         `json:"max_message_size" yaml:"max_message_size"`
	ReadBufferSize    int           `json:"read_buffer_size" yaml:"read_buffer_size"`
	WriteBufferSize   int           `json:"write_buffer_size" yaml:"write_buffer_size"`
	EnableCompression bool          `json:"enable_compression" yaml:"enable_compression"`
}

type ResponseCacheConfig struct {
	Enabled            bool          `json:"enabled" yaml:"enabled"`
	TTL                time.Duration `json:"ttl" yaml:"ttl"`
	MaxSize            int64         `json:"max_size" yaml:"max_size"`
	MaxEntries         int           `json:"max_entries" yaml:"max_entries"`
	CacheKeyHeader     string        `json:"cache_key_header" yaml:"cache_key_header"`
	InvalidationHeader string        `json:"invalidation_header" yaml:"invalidation_header"`
}

type GeoRoutingConfig struct {
	Enabled         bool          `json:"enabled" yaml:"enabled"`
	DatabasePath    string        `json:"database_path" yaml:"database_path"`
	CacheExpiry     time.Duration `json:"cache_expiry" yaml:"cache_expiry"`
	DefaultRegion   string        `json:"default_region" yaml:"default_region"`
	FallbackBackend string        `json:"fallback_backend" yaml:"fallback_backend"`
}

type AdaptiveConfig struct {
	Enabled             bool          `json:"enabled" yaml:"enabled"`
	Algorithm           string        `json:"algorithm" yaml:"algorithm"`
	WindowSize          int           `json:"window_size" yaml:"window_size"`
	UpdateInterval      time.Duration `json:"update_interval" yaml:"update_interval"`
	ResponseTimeWeight  float64       `json:"response_time_weight" yaml:"response_time_weight"`
	ErrorRateWeight     float64       `json:"error_rate_weight" yaml:"error_rate_weight"`
	ConnectionWeight    float64       `json:"connection_weight" yaml:"connection_weight"`
	PredictionHorizon   time.Duration `json:"prediction_horizon" yaml:"prediction_horizon"`
	MinConfidence       float64       `json:"min_confidence" yaml:"min_confidence"`
	AdaptationThreshold float64       `json:"adaptation_threshold" yaml:"adaptation_threshold"`
}

type TransformationConfig struct {
	Enabled            bool          `json:"enabled" yaml:"enabled"`
	MaxBodySize        int64         `json:"max_body_size" yaml:"max_body_size"`
	Timeout            time.Duration `json:"timeout" yaml:"timeout"`
	PreserveOriginal   bool          `json:"preserve_original" yaml:"preserve_original"`
	LogTransformations bool          `json:"log_transformations" yaml:"log_transformations"`
}

type BlueGreenConfig struct {
	Enabled             bool          `json:"enabled" yaml:"enabled"`
	DefaultEnvironment  string        `json:"default_environment" yaml:"default_environment"`
	HealthCheckInterval time.Duration `json:"health_check_interval" yaml:"health_check_interval"`
	DeploymentTimeout   time.Duration `json:"deployment_timeout" yaml:"deployment_timeout"`
	RollbackTimeout     time.Duration `json:"rollback_timeout" yaml:"rollback_timeout"`
	AutoPromotion       bool          `json:"auto_promotion" yaml:"auto_promotion"`
	HealthThreshold     float64       `json:"health_threshold" yaml:"health_threshold"`
	TrafficShiftMode    string        `json:"traffic_shift_mode" yaml:"traffic_shift_mode"`
}

type APIGatewayConfig struct {
	Enabled          bool            `json:"enabled" yaml:"enabled"`
	DefaultQuota     QuotaConfig     `json:"default_quota" yaml:"default_quota"`
	RateLimitConfig  RateLimitConfig `json:"rate_limit_config" yaml:"rate_limit_config"`
	AuthRequired     bool            `json:"auth_required" yaml:"auth_required"`
	KeyValidation    bool            `json:"key_validation" yaml:"key_validation"`
	QuotaEnforcement bool            `json:"quota_enforcement" yaml:"quota_enforcement"`
	MetricsEnabled   bool            `json:"metrics_enabled" yaml:"metrics_enabled"`
}

type TracingConfig struct {
	Enabled           bool          `json:"enabled" yaml:"enabled"`
	ServiceName       string        `json:"service_name" yaml:"service_name"`
	SamplingRate      float64       `json:"sampling_rate" yaml:"sampling_rate"`
	FlushInterval     time.Duration `json:"flush_interval" yaml:"flush_interval"`
	MaxSpansPerTrace  int           `json:"max_spans_per_trace" yaml:"max_spans_per_trace"`
	PropagationFormat string        `json:"propagation_format" yaml:"propagation_format"`
}

type QuotaConfig struct {
	TimeWindow   time.Duration `json:"time_window" yaml:"time_window"`
	DefaultLimit int64         `json:"default_limit" yaml:"default_limit"`
	ResetPolicy  string        `json:"reset_policy" yaml:"reset_policy"`
	MaxLimit     int64         `json:"max_limit" yaml:"max_limit"`
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:           8080,
		RequestTimeout: 30,
		TLS: TLSConfig{
			CertFile: "",
			KeyFile:  "",
		},
		Backends: []BackendConfig{
			{
				URL:             "http://localhost:8081",
				Weight:          1,
				MaxConnections:  100,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
			{
				URL:             "http://localhost:8082",
				Weight:          1,
				MaxConnections:  100,
				HealthCheckPath: "/health",
				Timeout:         5 * time.Second,
			},
		},
		HealthCheck: HealthCheckConfig{
			Interval:         10 * time.Second,
			Timeout:          2 * time.Second,
			FailureThreshold: 3,
			SuccessThreshold: 2,
			Path:             "/health",
		},
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			BurstSize:         200,
			Window:            time.Minute,
		},
		Logging: LoggingConfig{
			Level:      "info",
			Format:     "json",
			Output:     "stdout",
			Structured: true,
		},
		Metrics: MetricsConfig{
			Port: 9090,
			Path: "/metrics",
		},
		Routing: RoutingConfig{
			Algorithm:        "round_robin",
			StickySessions:   false,
			CookieName:       "lb_session",
			AdaptiveRouting:  true,
			CircuitBreaker:   true,
			FailureThreshold: 5,
		},
		Pool: PoolConfig{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Try to load from config file if it exists
	if data, err := os.ReadFile("config.json"); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	return cfg, nil
}
