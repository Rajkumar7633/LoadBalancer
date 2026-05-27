package advanced_routing

import (
	"net/http"
	"regexp"
	"strings"

	"loadbalancer/internal/backend"
)

type ContentBasedRouter struct {
	rules []RoutingRule
}

type RoutingRule struct {
	Name        string
	Condition   RouteCondition
	Backends    []backend.Backend
	Priority    int
	Description string
}

type RouteCondition struct {
	PathMatch    *PathMatch    `json:"path_match,omitempty"`
	HeaderMatch  *HeaderMatch  `json:"header_match,omitempty"`
	QueryMatch   *QueryMatch   `json:"query_match,omitempty"`
	MethodMatch  []string      `json:"method_match,omitempty"`
	BodyMatch    *BodyMatch    `json:"body_match,omitempty"`
}

type PathMatch struct {
	Type   string   `json:"type"`   // "exact", "prefix", "regex", "suffix"
	Values []string `json:"values"`
}

type HeaderMatch struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`    // "exact", "regex", "contains"
	Values  []string `json:"values"`
}

type QueryMatch struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`    // "exact", "regex", "contains"
	Values  []string `json:"values"`
}

type BodyMatch struct {
	Type     string `json:"type"`     // "json", "regex", "contains"
	Field    string `json:"field"`    // for JSON type
	Pattern  string `json:"pattern"`
}

func NewContentBasedRouter() *ContentBasedRouter {
	return &ContentBasedRouter{
		rules: make([]RoutingRule, 0),
	}
}

func (cbr *ContentBasedRouter) AddRule(rule RoutingRule) {
	cbr.rules = append(cbr.rules, rule)
	// Sort rules by priority (higher priority first)
	for i := 0; i < len(cbr.rules)-1; i++ {
		for j := i + 1; j < len(cbr.rules); j++ {
			if cbr.rules[i].Priority < cbr.rules[j].Priority {
				cbr.rules[i], cbr.rules[j] = cbr.rules[j], cbr.rules[i]
			}
		}
	}
}

func (cbr *ContentBasedRouter) Route(r *http.Request) []backend.Backend {
	for _, rule := range cbr.rules {
		if cbr.matchesCondition(r, rule.Condition) {
			return rule.Backends
		}
	}
	return nil
}

func (cbr *ContentBasedRouter) matchesCondition(r *http.Request, condition RouteCondition) bool {
	// Check method match
	if len(condition.MethodMatch) > 0 {
		methodMatched := false
		for _, method := range condition.MethodMatch {
			if r.Method == method {
				methodMatched = true
				break
			}
		}
		if !methodMatched {
			return false
		}
	}

	// Check path match
	if condition.PathMatch != nil {
		if !cbr.matchPath(r.URL.Path, condition.PathMatch) {
			return false
		}
	}

	// Check header match
	if condition.HeaderMatch != nil {
		if !cbr.matchHeader(r.Header, condition.HeaderMatch) {
			return false
		}
	}

	// Check query match
	if condition.QueryMatch != nil {
		if !cbr.matchQuery(r.URL.Query(), condition.QueryMatch) {
			return false
		}
	}

	// Check body match (would need to read body, skip for now)
	if condition.BodyMatch != nil {
		// TODO: Implement body matching
		return false
	}

	return true
}

func (cbr *ContentBasedRouter) matchPath(path string, pathMatch *PathMatch) bool {
	switch pathMatch.Type {
	case "exact":
		for _, value := range pathMatch.Values {
			if path == value {
				return true
			}
		}
	case "prefix":
		for _, value := range pathMatch.Values {
			if strings.HasPrefix(path, value) {
				return true
			}
		}
	case "suffix":
		for _, value := range pathMatch.Values {
			if strings.HasSuffix(path, value) {
				return true
			}
		}
	case "regex":
		for _, value := range pathMatch.Values {
			if matched, _ := regexp.MatchString(value, path); matched {
				return true
			}
		}
	}
	return false
}

func (cbr *ContentBasedRouter) matchHeader(headers http.Header, headerMatch *HeaderMatch) bool {
	values := headers[headerMatch.Name]
	if len(values) == 0 {
		return false
	}

	switch headerMatch.Type {
	case "exact":
		for _, headerValue := range values {
			for _, matchValue := range headerMatch.Values {
				if headerValue == matchValue {
					return true
				}
			}
		}
	case "contains":
		for _, headerValue := range values {
			for _, matchValue := range headerMatch.Values {
				if strings.Contains(headerValue, matchValue) {
					return true
				}
			}
		}
	case "regex":
		for _, headerValue := range values {
			for _, matchValue := range headerMatch.Values {
				if matched, _ := regexp.MatchString(matchValue, headerValue); matched {
					return true
				}
			}
		}
	}
	return false
}

func (cbr *ContentBasedRouter) matchQuery(query map[string][]string, queryMatch *QueryMatch) bool {
	values := query[queryMatch.Name]
	if len(values) == 0 {
		return false
	}

	switch queryMatch.Type {
	case "exact":
		for _, queryValue := range values {
			for _, matchValue := range queryMatch.Values {
				if queryValue == matchValue {
					return true
				}
			}
		}
	case "contains":
		for _, queryValue := range values {
			for _, matchValue := range queryMatch.Values {
				if strings.Contains(queryValue, matchValue) {
					return true
				}
			}
		}
	case "regex":
		for _, queryValue := range values {
			for _, matchValue := range queryMatch.Values {
				if matched, _ := regexp.MatchString(matchValue, queryValue); matched {
					return true
				}
			}
		}
	}
	return false
}
