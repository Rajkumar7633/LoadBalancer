package advanced_routing

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type DistributedTracer struct {
	providers map[string]TraceProvider
	sampler   TraceSampler
	config    TracingConfig
	mu        sync.RWMutex
}

type TraceProvider interface {
	CreateSpan(ctx context.Context, name string, opts ...SpanOption) (Span, context.Context)
	InjectHeaders(span Span, headers http.Header)
	ExtractHeaders(headers http.Header) (SpanContext, error)
	Flush(ctx context.Context) error
}

type Span interface {
	SetTag(key string, value interface{})
	SetBaggageItem(key, value string)
	GetBaggageItem(key string) string
	LogEvent(event string)
	LogEventWithPayload(event string, payload interface{})
	Finish()
	Context() SpanContext
	TraceID() string
	SpanID() string
}

type SpanContext interface {
	TraceID() string
	SpanID() string
	Baggage() map[string]string
	IsValid() bool
}

type TraceSampler interface {
	ShouldSample(traceID string) bool
	GetSamplingRate() float64
}

type TracingConfig struct {
	Enabled           bool          `json:"enabled"`
	ServiceName       string        `json:"service_name"`
	SamplingRate      float64       `json:"sampling_rate"`
	FlushInterval     time.Duration `json:"flush_interval"`
	MaxSpansPerTrace  int           `json:"max_spans_per_trace"`
	PropagationFormat string        `json:"propagation_format"` // "b3", "jaeger", "w3c"
}

type SpanOption struct {
	Key   string
	Value interface{}
}

type TraceContext struct {
	TraceID  string                 `json:"trace_id"`
	SpanID   string                 `json:"span_id"`
	ParentID string                 `json:"parent_id"`
	Baggage  map[string]string      `json:"baggage"`
	Sampled  bool                   `json:"sampled"`
	Flags    map[string]interface{} `json:"flags"`
}

type JaegerProvider struct {
	serviceName string
	endpoint    string
	sampler     TraceSampler
	spans       []JaegerSpan
	mu          sync.Mutex
}

type JaegerSpan struct {
	TraceID      string                 `json:"trace_id"`
	SpanID       string                 `json:"span_id"`
	ParentSpanID string                 `json:"parent_span_id"`
	Operation    string                 `json:"operation_name"`
	StartTime    time.Time              `json:"start_time"`
	Duration     time.Duration          `json:"duration"`
	Tags         map[string]interface{} `json:"tags"`
	Logs         []LogEntry             `json:"logs"`
	References   []SpanReference        `json:"references"`
	Status       SpanStatus             `json:"status"`
}

type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Fields    map[string]interface{} `json:"fields"`
}

type SpanReference struct {
	RefType string `json:"ref_type"` // "child_of", "follows_from"
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id"`
}

type SpanStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ProbabilisticSampler struct {
	samplingRate float64
}

type ConstantSampler struct {
	decision bool
}

type RateLimitingSampler struct {
	maxTracesPerSecond int
	credits            float64
	lastUpdate         time.Time
	mu                 sync.Mutex
}

func NewDistributedTracer(config TracingConfig) *DistributedTracer {
	var sampler TraceSampler
	switch {
	case config.SamplingRate >= 1.0:
		sampler = &ConstantSampler{decision: true}
	case config.SamplingRate <= 0.0:
		sampler = &ConstantSampler{decision: false}
	default:
		sampler = &ProbabilisticSampler{samplingRate: config.SamplingRate}
	}

	return &DistributedTracer{
		providers: make(map[string]TraceProvider),
		sampler:   sampler,
		config:    config,
	}
}

func (dt *DistributedTracer) AddProvider(name string, provider TraceProvider) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.providers[name] = provider
}

func (dt *DistributedTracer) StartSpan(ctx context.Context, operationName string, opts ...SpanOption) (Span, context.Context) {
	if !dt.config.Enabled {
		return &NoOpSpan{}, ctx
	}

	// Check if we should sample this trace
	traceID := dt.extractTraceID(ctx)
	if !dt.sampler.ShouldSample(traceID) {
		return &NoOpSpan{}, ctx
	}

	// Use the first available provider
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	for _, provider := range dt.providers {
		span, newCtx := provider.CreateSpan(ctx, operationName, opts...)
		return span, newCtx
	}

	return &NoOpSpan{}, ctx
}

func (dt *DistributedTracer) InjectSpan(span Span, headers http.Header) {
	if !dt.config.Enabled {
		return
	}

	dt.mu.RLock()
	defer dt.mu.RUnlock()

	for _, provider := range dt.providers {
		provider.InjectHeaders(span, headers)
		break // Use first provider
	}
}

func (dt *DistributedTracer) ExtractSpan(headers http.Header) (SpanContext, error) {
	if !dt.config.Enabled {
		return &NoOpSpanContext{}, nil
	}

	dt.mu.RLock()
	defer dt.mu.RUnlock()

	for _, provider := range dt.providers {
		ctx, err := provider.ExtractHeaders(headers)
		if err == nil {
			return ctx, nil
		}
	}

	return &NoOpSpanContext{}, fmt.Errorf("no valid trace context found")
}

func (dt *DistributedTracer) Flush(ctx context.Context) error {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	for _, provider := range dt.providers {
		if err := provider.Flush(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (dt *DistributedTracer) extractTraceID(ctx context.Context) string {
	// Extract trace ID from context if available
	if spanCtx, ok := ctx.Value("trace_context").(SpanContext); ok {
		return spanCtx.TraceID()
	}
	return ""
}

func (dt *DistributedTracer) GetStats() TracingStats {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	stats := TracingStats{
		ProvidersCount: len(dt.providers),
		Enabled:        dt.config.Enabled,
		SamplingRate:   dt.sampler.GetSamplingRate(),
	}

	for _, provider := range dt.providers {
		if jaeger, ok := provider.(*JaegerProvider); ok {
			stats.ActiveSpans = len(jaeger.spans)
		}
	}

	return stats
}

type TracingStats struct {
	ProvidersCount int     `json:"providers_count"`
	Enabled        bool    `json:"enabled"`
	SamplingRate   float64 `json:"sampling_rate"`
	ActiveSpans    int     `json:"active_spans"`
}

// JaegerProvider implementation
func NewJaegerProvider(serviceName, endpoint string, sampler TraceSampler) *JaegerProvider {
	return &JaegerProvider{
		serviceName: serviceName,
		endpoint:    endpoint,
		sampler:     sampler,
		spans:       make([]JaegerSpan, 0),
	}
}

func (jp *JaegerProvider) CreateSpan(ctx context.Context, name string, opts ...SpanOption) (Span, context.Context) {
	jp.mu.Lock()
	defer jp.mu.Unlock()

	span := &JaegerSpanImpl{
		span: JaegerSpan{
			TraceID:   jp.generateTraceID(ctx),
			SpanID:    jp.generateSpanID(),
			Operation: name,
			StartTime: time.Now(),
			Tags:      make(map[string]interface{}),
			Logs:      make([]LogEntry, 0),
			Status:    SpanStatus{Code: 0},
		},
		provider: jp,
	}

	// Apply options
	for _, opt := range opts {
		span.SetTag(opt.Key, opt.Value)
	}

	// Set default tags
	span.SetTag("service.name", jp.serviceName)
	span.SetTag("span.kind", "server")

	jp.spans = append(jp.spans, span.span)

	// Create new context with span
	newCtx := context.WithValue(ctx, "span", span)
	return span, newCtx
}

func (jp *JaegerProvider) InjectHeaders(span Span, headers http.Header) {
	spanCtx := span.Context()
	headers.Set("X-Trace-Id", spanCtx.TraceID())
	headers.Set("X-Span-Id", spanCtx.SpanID())

	// Inject baggage
	for key, value := range spanCtx.Baggage() {
		headers.Set("X-Baggage-"+key, value)
	}
}

func (jp *JaegerProvider) ExtractHeaders(headers http.Header) (SpanContext, error) {
	traceID := headers.Get("X-Trace-Id")
	spanID := headers.Get("X-Span-Id")

	if traceID == "" || spanID == "" {
		return &NoOpSpanContext{}, fmt.Errorf("missing trace headers")
	}

	baggage := make(map[string]string)
	for key, value := range headers {
		if len(key) > 10 && key[:10] == "X-Baggage-" {
			baggageKey := key[10:]
			baggage[baggageKey] = value[0]
		}
	}

	return &JaegerSpanContext{
		traceID: traceID,
		spanID:  spanID,
		baggage: baggage,
	}, nil
}

func (jp *JaegerProvider) Flush(ctx context.Context) error {
	jp.mu.Lock()
	defer jp.mu.Unlock()

	// In a real implementation, send spans to Jaeger collector
	jp.spans = make([]JaegerSpan, 0)
	return nil
}

func (jp *JaegerProvider) generateTraceID(ctx context.Context) string {
	// Generate trace ID or extract from context
	if spanCtx, ok := ctx.Value("trace_context").(SpanContext); ok && spanCtx.IsValid() {
		return spanCtx.TraceID()
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func (jp *JaegerProvider) generateSpanID() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

// JaegerSpanImpl implementation
type JaegerSpanImpl struct {
	span     JaegerSpan
	provider *JaegerProvider
}

func (js *JaegerSpanImpl) SetTag(key string, value interface{}) {
	js.span.Tags[key] = value
}

func (js *JaegerSpanImpl) SetBaggageItem(key, value string) {
	// Baggage is handled at the context level
}

func (js *JaegerSpanImpl) GetBaggageItem(key string) string {
	return ""
}

func (js *JaegerSpanImpl) LogEvent(event string) {
	js.LogEventWithPayload(event, nil)
}

func (js *JaegerSpanImpl) LogEventWithPayload(event string, payload interface{}) {
	log := LogEntry{
		Timestamp: time.Now(),
		Fields: map[string]interface{}{
			"event": event,
		},
	}
	if payload != nil {
		log.Fields["payload"] = payload
	}
	js.span.Logs = append(js.span.Logs, log)
}

func (js *JaegerSpanImpl) Finish() {
	js.span.Duration = time.Since(js.span.StartTime)
}

func (js *JaegerSpanImpl) Context() SpanContext {
	return &JaegerSpanContext{
		traceID: js.span.TraceID,
		spanID:  js.span.SpanID,
		baggage: make(map[string]string),
	}
}

func (js *JaegerSpanImpl) TraceID() string {
	return js.span.TraceID
}

func (js *JaegerSpanImpl) SpanID() string {
	return js.span.SpanID
}

// JaegerSpanContext implementation
type JaegerSpanContext struct {
	traceID string
	spanID  string
	baggage map[string]string
}

func (jsc *JaegerSpanContext) TraceID() string {
	return jsc.traceID
}

func (jsc *JaegerSpanContext) SpanID() string {
	return jsc.spanID
}

func (jsc *JaegerSpanContext) Baggage() map[string]string {
	return jsc.baggage
}

func (jsc *JaegerSpanContext) IsValid() bool {
	return jsc.traceID != "" && jsc.spanID != ""
}

// Sampler implementations
func (ps *ProbabilisticSampler) ShouldSample(traceID string) bool {
	// In a real implementation, use deterministic sampling based on traceID
	return true // Simplified
}

func (ps *ProbabilisticSampler) GetSamplingRate() float64 {
	return ps.samplingRate
}

func (cs *ConstantSampler) ShouldSample(traceID string) bool {
	return cs.decision
}

func (cs *ConstantSampler) GetSamplingRate() float64 {
	if cs.decision {
		return 1.0
	}
	return 0.0
}

func (rls *RateLimitingSampler) ShouldSample(traceID string) bool {
	rls.mu.Lock()
	defer rls.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rls.lastUpdate)
	rls.lastUpdate = now

	// Add credits based on elapsed time
	rls.credits += float64(elapsed.Seconds()) * float64(rls.maxTracesPerSecond)
	if rls.credits > float64(rls.maxTracesPerSecond) {
		rls.credits = float64(rls.maxTracesPerSecond)
	}

	if rls.credits >= 1.0 {
		rls.credits -= 1.0
		return true
	}
	return false
}

func (rls *RateLimitingSampler) GetSamplingRate() float64 {
	return float64(rls.maxTracesPerSecond)
}

// No-op implementations for when tracing is disabled
type NoOpSpan struct{}

func (n *NoOpSpan) SetTag(key string, value interface{})                  {}
func (n *NoOpSpan) SetBaggageItem(key, value string)                      {}
func (n *NoOpSpan) GetBaggageItem(key string) string                      { return "" }
func (n *NoOpSpan) LogEvent(event string)                                 {}
func (n *NoOpSpan) LogEventWithPayload(event string, payload interface{}) {}
func (n *NoOpSpan) Finish()                                               {}
func (n *NoOpSpan) Context() SpanContext                                  { return &NoOpSpanContext{} }
func (n *NoOpSpan) TraceID() string                                       { return "" }
func (n *NoOpSpan) SpanID() string                                        { return "" }

type NoOpSpanContext struct{}

func (n *NoOpSpanContext) TraceID() string            { return "" }
func (n *NoOpSpanContext) SpanID() string             { return "" }
func (n *NoOpSpanContext) Baggage() map[string]string { return nil }
func (n *NoOpSpanContext) IsValid() bool              { return false }
