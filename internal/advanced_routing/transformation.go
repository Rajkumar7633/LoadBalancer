package advanced_routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type TransformationEngine struct {
	rules      []TransformationRule
	middleware []Middleware
	config     TransformationConfig
	mu         sync.RWMutex
}

type TransformationRule struct {
	Name        string                   `json:"name"`
	Priority    int                      `json:"priority"`
	Conditions  TransformationConditions `json:"conditions"`
	Actions     []TransformationAction   `json:"actions"`
	Enabled     bool                     `json:"enabled"`
	Description string                   `json:"description"`
}

type TransformationConditions struct {
	PathMatch   *StringMatch            `json:"path_match"`
	MethodMatch []string                `json:"method_match"`
	HeaderMatch map[string]*StringMatch `json:"header_match"`
	QueryMatch  map[string]*StringMatch `json:"query_match"`
	BodyMatch   *StringMatch            `json:"body_match"`
	ContentType []string                `json:"content_type"`
	UserAgent   *StringMatch            `json:"user_agent"`
}

type TransformationAction struct {
	Type       string                 `json:"type"` // "add_header", "remove_header", "rewrite_path", "transform_body", "redirect"
	Parameters map[string]interface{} `json:"parameters"`
	StopChain  bool                   `json:"stop_chain"`
}

type StringMatch struct {
	Type   string   `json:"type"` // "exact", "prefix", "suffix", "regex", "contains"
	Values []string `json:"values"`
}

type Middleware struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"` // "request", "response", "both"
	Handler  MiddlewareHandler `json:"-"`
	Priority int               `json:"priority"`
	Enabled  bool              `json:"enabled"`
}

type MiddlewareHandler func(ctx context.Context, req *http.Request, resp *http.Response) error

type TransformationConfig struct {
	Enabled            bool          `json:"enabled"`
	MaxBodySize        int64         `json:"max_body_size"`
	Timeout            time.Duration `json:"timeout"`
	PreserveOriginal   bool          `json:"preserve_original"`
	LogTransformations bool          `json:"log_transformations"`
}

type TransformedRequest struct {
	Original        *http.Request
	Modified        *http.Request
	Transformations []string
	Timestamp       time.Time
}

type TransformedResponse struct {
	Original        *http.Response
	Modified        *http.Response
	Transformations []string
	Timestamp       time.Time
}

func NewTransformationEngine(config TransformationConfig) *TransformationEngine {
	return &TransformationEngine{
		rules:      make([]TransformationRule, 0),
		middleware: make([]Middleware, 0),
		config:     config,
	}
}

func (te *TransformationEngine) AddRule(rule TransformationRule) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.rules = append(te.rules, rule)
}

func (te *TransformationEngine) AddMiddleware(middleware Middleware) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.middleware = append(te.middleware, middleware)
}

func (te *TransformationEngine) TransformRequest(ctx context.Context, req *http.Request) (*TransformedRequest, error) {
	if !te.config.Enabled {
		return &TransformedRequest{
			Original:        req,
			Modified:        req,
			Transformations: []string{},
			Timestamp:       time.Now(),
		}, nil
	}

	transformedReq := req.Clone(ctx)
	transformations := make([]string, 0)

	// Apply transformation rules
	sortedRules := te.getSortedRules()
	for _, rule := range sortedRules {
		if !rule.Enabled {
			continue
		}

		if te.matchesConditions(req, rule.Conditions) {
			for _, action := range rule.Actions {
				if err := te.applyRequestAction(transformedReq, action); err != nil {
					return nil, fmt.Errorf("failed to apply action %s: %w", action.Type, err)
				}
				transformations = append(transformations, fmt.Sprintf("%s:%s", rule.Name, action.Type))

				if action.StopChain {
					break
				}
			}
		}
	}

	// Apply request middleware
	for _, middleware := range te.middleware {
		if !middleware.Enabled || (middleware.Type != "request" && middleware.Type != "both") {
			continue
		}

		if err := middleware.Handler(ctx, transformedReq, nil); err != nil {
			return nil, fmt.Errorf("middleware %s failed: %w", middleware.Name, err)
		}
		transformations = append(transformations, fmt.Sprintf("middleware:%s", middleware.Name))
	}

	return &TransformedRequest{
		Original:        req,
		Modified:        transformedReq,
		Transformations: transformations,
		Timestamp:       time.Now(),
	}, nil
}

func (te *TransformationEngine) TransformResponse(ctx context.Context, req *http.Request, resp *http.Response) (*TransformedResponse, error) {
	if !te.config.Enabled {
		return &TransformedResponse{
			Original:        resp,
			Modified:        resp,
			Transformations: []string{},
			Timestamp:       time.Now(),
		}, nil
	}

	transformedResp := te.cloneResponse(resp)
	transformations := make([]string, 0)

	// Apply transformation rules for response
	sortedRules := te.getSortedRules()
	for _, rule := range sortedRules {
		if !rule.Enabled {
			continue
		}

		if te.matchesConditions(req, rule.Conditions) {
			for _, action := range rule.Actions {
				if err := te.applyResponseAction(transformedResp, action); err != nil {
					return nil, fmt.Errorf("failed to apply response action %s: %w", action.Type, err)
				}
				transformations = append(transformations, fmt.Sprintf("%s:%s", rule.Name, action.Type))

				if action.StopChain {
					break
				}
			}
		}
	}

	// Apply response middleware
	for _, middleware := range te.middleware {
		if !middleware.Enabled || (middleware.Type != "response" && middleware.Type != "both") {
			continue
		}

		if err := middleware.Handler(ctx, req, transformedResp); err != nil {
			return nil, fmt.Errorf("response middleware %s failed: %w", middleware.Name, err)
		}
		transformations = append(transformations, fmt.Sprintf("middleware:%s", middleware.Name))
	}

	return &TransformedResponse{
		Original:        resp,
		Modified:        transformedResp,
		Transformations: transformations,
		Timestamp:       time.Now(),
	}, nil
}

func (te *TransformationEngine) matchesConditions(req *http.Request, conditions TransformationConditions) bool {
	// Check path match
	if conditions.PathMatch != nil {
		if !te.matchesString(req.URL.Path, *conditions.PathMatch) {
			return false
		}
	}

	// Check method match
	if len(conditions.MethodMatch) > 0 {
		methodMatched := false
		for _, method := range conditions.MethodMatch {
			if strings.EqualFold(req.Method, method) {
				methodMatched = true
				break
			}
		}
		if !methodMatched {
			return false
		}
	}

	// Check header match
	for headerName, match := range conditions.HeaderMatch {
		headerValue := req.Header.Get(headerName)
		if !te.matchesString(headerValue, *match) {
			return false
		}
	}

	// Check query match
	for queryName, match := range conditions.QueryMatch {
		queryValue := req.URL.Query().Get(queryName)
		if !te.matchesString(queryValue, *match) {
			return false
		}
	}

	// Check content type
	if len(conditions.ContentType) > 0 {
		contentType := req.Header.Get("Content-Type")
		contentTypeMatched := false
		for _, ct := range conditions.ContentType {
			if strings.Contains(strings.ToLower(contentType), strings.ToLower(ct)) {
				contentTypeMatched = true
				break
			}
		}
		if !contentTypeMatched {
			return false
		}
	}

	// Check user agent
	if conditions.UserAgent != nil {
		userAgent := req.Header.Get("User-Agent")
		if !te.matchesString(userAgent, *conditions.UserAgent) {
			return false
		}
	}

	return true
}

func (te *TransformationEngine) matchesString(value string, match StringMatch) bool {
	switch match.Type {
	case "exact":
		for _, v := range match.Values {
			if value == v {
				return true
			}
		}
	case "prefix":
		for _, v := range match.Values {
			if strings.HasPrefix(value, v) {
				return true
			}
		}
	case "suffix":
		for _, v := range match.Values {
			if strings.HasSuffix(value, v) {
				return true
			}
		}
	case "contains":
		for _, v := range match.Values {
			if strings.Contains(value, v) {
				return true
			}
		}
	case "regex":
		for _, v := range match.Values {
			if matched, _ := regexp.MatchString(v, value); matched {
				return true
			}
		}
	}
	return false
}

func (te *TransformationEngine) applyRequestAction(req *http.Request, action TransformationAction) error {
	switch action.Type {
	case "add_header":
		return te.addRequestHeader(req, action.Parameters)
	case "remove_header":
		return te.removeRequestHeader(req, action.Parameters)
	case "rewrite_path":
		return te.rewritePath(req, action.Parameters)
	case "transform_body":
		return te.transformRequestBody(req, action.Parameters)
	case "redirect":
		return te.createRedirect(req, action.Parameters)
	default:
		return fmt.Errorf("unknown action type: %s", action.Type)
	}
}

func (te *TransformationEngine) applyResponseAction(resp *http.Response, action TransformationAction) error {
	switch action.Type {
	case "add_header":
		return te.addResponseHeader(resp, action.Parameters)
	case "remove_header":
		return te.removeResponseHeader(resp, action.Parameters)
	case "transform_body":
		return te.transformResponseBody(resp, action.Parameters)
	case "set_status":
		return te.setResponseStatus(resp, action.Parameters)
	default:
		return fmt.Errorf("unknown response action type: %s", action.Type)
	}
}

func (te *TransformationEngine) addRequestHeader(req *http.Request, params map[string]interface{}) error {
	name, ok := params["name"].(string)
	if !ok {
		return fmt.Errorf("header name required")
	}
	value, ok := params["value"].(string)
	if !ok {
		return fmt.Errorf("header value required")
	}
	req.Header.Set(name, value)
	return nil
}

func (te *TransformationEngine) removeRequestHeader(req *http.Request, params map[string]interface{}) error {
	name, ok := params["name"].(string)
	if !ok {
		return fmt.Errorf("header name required")
	}
	req.Header.Del(name)
	return nil
}

func (te *TransformationEngine) addResponseHeader(resp *http.Response, params map[string]interface{}) error {
	name, ok := params["name"].(string)
	if !ok {
		return fmt.Errorf("header name required")
	}
	value, ok := params["value"].(string)
	if !ok {
		return fmt.Errorf("header value required")
	}
	resp.Header.Set(name, value)
	return nil
}

func (te *TransformationEngine) removeResponseHeader(resp *http.Response, params map[string]interface{}) error {
	name, ok := params["name"].(string)
	if !ok {
		return fmt.Errorf("header name required")
	}
	resp.Header.Del(name)
	return nil
}

func (te *TransformationEngine) rewritePath(req *http.Request, params map[string]interface{}) error {
	pattern, ok := params["pattern"].(string)
	if !ok {
		return fmt.Errorf("pattern required")
	}
	replacement, ok := params["replacement"].(string)
	if !ok {
		return fmt.Errorf("replacement required")
	}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid regex pattern: %w", err)
	}

	newPath := regex.ReplaceAllString(req.URL.Path, replacement)
	req.URL.Path = newPath
	return nil
}

func (te *TransformationEngine) transformRequestBody(req *http.Request, params map[string]interface{}) error {
	if req.Body == nil {
		return nil
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}

	// Apply transformation based on type
	transformType, ok := params["type"].(string)
	if !ok {
		return fmt.Errorf("transformation type required")
	}

	var transformedBody []byte
	switch transformType {
	case "json":
		transformedBody, err = te.transformJSON(body, params)
	case "xml":
		transformedBody, err = te.transformXML(body, params)
	case "text":
		transformedBody, err = te.transformText(body, params)
	default:
		return fmt.Errorf("unsupported transformation type: %s", transformType)
	}

	if err != nil {
		return fmt.Errorf("failed to transform body: %w", err)
	}

	req.Body = io.NopCloser(bytes.NewReader(transformedBody))
	req.ContentLength = int64(len(transformedBody))
	return nil
}

func (te *TransformationEngine) transformResponseBody(resp *http.Response, params map[string]interface{}) error {
	if resp.Body == nil {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Apply transformation based on type
	transformType, ok := params["type"].(string)
	if !ok {
		return fmt.Errorf("transformation type required")
	}

	var transformedBody []byte
	switch transformType {
	case "json":
		transformedBody, err = te.transformJSON(body, params)
	case "xml":
		transformedBody, err = te.transformXML(body, params)
	case "text":
		transformedBody, err = te.transformText(body, params)
	default:
		return fmt.Errorf("unsupported transformation type: %s", transformType)
	}

	if err != nil {
		return fmt.Errorf("failed to transform body: %w", err)
	}

	resp.Body = io.NopCloser(bytes.NewReader(transformedBody))
	resp.ContentLength = int64(len(transformedBody))
	return nil
}

func (te *TransformationEngine) transformJSON(body []byte, params map[string]interface{}) ([]byte, error) {
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Apply JSON transformations
	if operations, ok := params["operations"].([]interface{}); ok {
		for _, op := range operations {
			if opMap, ok := op.(map[string]interface{}); ok {
				if err := te.applyJSONOperation(&data, opMap); err != nil {
					return nil, err
				}
			}
		}
	}

	return json.Marshal(data)
}

func (te *TransformationEngine) transformXML(body []byte, params map[string]interface{}) ([]byte, error) {
	// Simplified XML transformation - in practice, use a proper XML library
	return body, nil
}

func (te *TransformationEngine) transformText(body []byte, params map[string]interface{}) ([]byte, error) {
	content := string(body)

	// Apply text transformations
	if find, ok := params["find"].(string); ok {
		replace, ok := params["replace"].(string)
		if ok {
			content = strings.ReplaceAll(content, find, replace)
		}
	}

	return []byte(content), nil
}

func (te *TransformationEngine) applyJSONOperation(data *interface{}, op map[string]interface{}) error {
	opType, ok := op["type"].(string)
	if !ok {
		return fmt.Errorf("operation type required")
	}

	switch opType {
	case "set":
		return te.setJSONValue(data, op)
	case "remove":
		return te.removeJSONValue(data, op)
	case "rename":
		return te.renameJSONKey(data, op)
	default:
		return fmt.Errorf("unknown operation type: %s", opType)
	}
}

func (te *TransformationEngine) setJSONValue(data *interface{}, op map[string]interface{}) error {
	path, ok := op["path"].(string)
	if !ok {
		return fmt.Errorf("path required")
	}
	value, ok := op["value"]
	if !ok {
		return fmt.Errorf("value required")
	}

	// Simplified JSON path implementation
	// In practice, use a proper JSON path library
	if path == "." {
		*data = value
	}
	return nil
}

func (te *TransformationEngine) removeJSONValue(data *interface{}, op map[string]interface{}) error {
	path, ok := op["path"].(string)
	if !ok {
		return fmt.Errorf("path required")
	}

	// Simplified implementation
	_ = path // TODO: implement proper JSON path removal
	return nil
}

func (te *TransformationEngine) renameJSONKey(data *interface{}, op map[string]interface{}) error {
	from, ok := op["from"].(string)
	if !ok {
		return fmt.Errorf("from key required")
	}
	to, ok := op["to"].(string)
	if !ok {
		return fmt.Errorf("to key required")
	}

	// Simplified implementation
	_ = from
	_ = to // TODO: implement proper JSON key renaming
	return nil
}

func (te *TransformationEngine) createRedirect(req *http.Request, params map[string]interface{}) error {
	url, ok := params["url"].(string)
	if !ok {
		return fmt.Errorf("redirect URL required")
	}

	statusCode, ok := params["status_code"].(float64)
	if !ok {
		statusCode = 302 // Default to temporary redirect
	}

	// This would be handled at the HTTP handler level
	_ = url
	_ = statusCode
	return nil
}

func (te *TransformationEngine) setResponseStatus(resp *http.Response, params map[string]interface{}) error {
	statusCode, ok := params["status_code"].(float64)
	if !ok {
		return fmt.Errorf("status code required")
	}

	resp.StatusCode = int(statusCode)
	resp.Status = fmt.Sprintf("%d %s", statusCode, http.StatusText(int(statusCode)))
	return nil
}

func (te *TransformationEngine) cloneResponse(resp *http.Response) *http.Response {
	clone := &http.Response{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Proto:      resp.Proto,
		ProtoMajor: resp.ProtoMajor,
		ProtoMinor: resp.ProtoMinor,
		Header:     make(http.Header),
		Close:      resp.Close,
	}

	// Copy headers
	for k, v := range resp.Header {
		clone.Header[k] = v
	}

	// Copy body
	if resp.Body != nil {
		body, _ := io.ReadAll(resp.Body)
		clone.Body = io.NopCloser(bytes.NewReader(body))
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}

	return clone
}

func (te *TransformationEngine) getSortedRules() []TransformationRule {
	sorted := make([]TransformationRule, len(te.rules))
	copy(sorted, te.rules)

	// Sort by priority (higher first)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].Priority < sorted[j].Priority {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

func (te *TransformationEngine) GetStats() TransformationStats {
	te.mu.RLock()
	defer te.mu.RUnlock()

	return TransformationStats{
		RulesCount:      len(te.rules),
		MiddlewareCount: len(te.middleware),
		Enabled:         te.config.Enabled,
	}
}

type TransformationStats struct {
	RulesCount      int  `json:"rules_count"`
	MiddlewareCount int  `json:"middleware_count"`
	Enabled         bool `json:"enabled"`
}
