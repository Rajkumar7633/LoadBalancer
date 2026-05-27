package security

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"go.uber.org/zap"
)

// WAFManager implements Web Application Firewall functionality
type WAFManager struct {
	config WAFManagerConfig
	logger *zap.Logger
	rules  []WAFFRule
}

// WAFManagerConfig contains WAF configuration
type WAFManagerConfig struct {
	Rules  []string    `json:"rules"`
	Mode   string      `json:"mode"` // block, monitor, learn
	Logger *zap.Logger `json:"-"`
}

// WAFFRule represents a WAF rule
type WAFFRule struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"` // sql_injection, xss, path_traversal, etc.
	Pattern     *regexp.Regexp `json:"-"`
	Action      string         `json:"action"`   // block, log, redirect
	Severity    string         `json:"severity"` // low, medium, high, critical
	Description string         `json:"description"`
	Enabled     bool           `json:"enabled"`
}

// WAFResult represents the result of a WAF check
type WAFResult struct {
	Blocked   bool      `json:"blocked"`
	Rule      *WAFFRule `json:"rule"`
	Reason    string    `json:"reason"`
	RiskScore float64   `json:"risk_score"`
}

// NewWAFManager creates a new WAF manager
func NewWAFManager(config WAFManagerConfig) (*WAFManager, error) {
	wm := &WAFManager{
		config: config,
		logger: config.Logger,
	}

	// Load default rules if none provided
	if len(config.Rules) == 0 {
		config.Rules = []string{
			"sql_injection",
			"xss",
			"path_traversal",
			"command_injection",
			"file_inclusion",
		}
	}

	// Initialize rules
	if err := wm.loadRules(); err != nil {
		return nil, fmt.Errorf("failed to load WAF rules: %w", err)
	}

	wm.logger.Info("WAF manager initialized",
		zap.String("mode", config.Mode),
		zap.Int("rules", len(wm.rules)))

	return wm, nil
}

// loadRules loads WAF rules
func (wm *WAFManager) loadRules() error {
	// Define default WAF rules
	defaultRules := []WAFFRule{
		{
			ID:          "SQL_INJECTION_001",
			Name:        "SQL Injection Detection",
			Type:        "sql_injection",
			Pattern:     regexp.MustCompile(`(?i)(union|select|insert|update|delete|drop|create|alter|exec|execute|script|javascript|vbscript)\s+|' OR |' AND |1=1|1=0|--|#|/\*|\*/`),
			Action:      "block",
			Severity:    "high",
			Description: "Detects common SQL injection patterns",
			Enabled:     true,
		},
		{
			ID:          "XSS_001",
			Name:        "Cross-Site Scripting Detection",
			Type:        "xss",
			Pattern:     regexp.MustCompile(`(?i)(<script|javascript:|vbscript:|onload=|onerror=|onclick=|alert\(|document\.)`),
			Action:      "block",
			Severity:    "high",
			Description: "Detects XSS attack patterns",
			Enabled:     true,
		},
		{
			ID:          "PATH_TRAVERSAL_001",
			Name:        "Path Traversal Detection",
			Type:        "path_traversal",
			Pattern:     regexp.MustCompile(`(\.\./|\.\.\\|%2e%2e%2f|%2e%2e\\|/etc/passwd|/proc/self/environ)`),
			Action:      "block",
			Severity:    "high",
			Description: "Detects path traversal attempts",
			Enabled:     true,
		},
		{
			ID:          "COMMAND_INJECTION_001",
			Name:        "Command Injection Detection",
			Type:        "command_injection",
			Pattern:     regexp.MustCompile(`(?i)(;|\||&|` + "`" + `|\$\(|\${|nc |netcat |wget |curl |bash |sh |powershell )`),
			Action:      "block",
			Severity:    "critical",
			Description: "Detects command injection attempts",
			Enabled:     true,
		},
		{
			ID:          "FILE_INCLUSION_001",
			Name:        "File Inclusion Detection",
			Type:        "file_inclusion",
			Pattern:     regexp.MustCompile(`(?i)(include|require|file_get_contents|fopen|readfile)\s*\(\s*['"]?\s*(http|ftp|php://|file://)`),
			Action:      "block",
			Severity:    "high",
			Description: "Detects file inclusion vulnerabilities",
			Enabled:     true,
		},
	}

	// Filter rules based on configuration
	for _, rule := range defaultRules {
		for _, ruleType := range wm.config.Rules {
			if rule.Type == ruleType {
				wm.rules = append(wm.rules, rule)
				break
			}
		}
	}

	return nil
}

// CheckRequest checks a request against WAF rules
func (wm *WAFManager) CheckRequest(req *http.Request, securityCtx *SecurityContext) error {
	// Collect request data for analysis
	requestData := wm.collectRequestData(req)

	// Check each enabled rule
	for _, rule := range wm.rules {
		if !rule.Enabled {
			continue
		}

		result := wm.checkRule(rule, requestData)
		if result.Blocked {
			wm.logWAFEvent(rule, req, securityCtx)

			if wm.config.Mode == "block" {
				return fmt.Errorf("request blocked by WAF rule %s: %s", rule.ID, result.Reason)
			} else if wm.config.Mode == "monitor" {
				wm.logger.Warn("WAF rule triggered (monitor mode)",
					zap.String("rule_id", rule.ID),
					zap.String("rule_name", rule.Name),
					zap.String("client_ip", securityCtx.ClientIP),
					zap.String("reason", result.Reason))
			}
		}
	}

	return nil
}

// collectRequestData collects data from the request for analysis
func (wm *WAFManager) collectRequestData(req *http.Request) map[string]string {
	data := make(map[string]string)

	// URL and query parameters
	data["url"] = req.URL.String()
	data["path"] = req.URL.Path
	data["query"] = req.URL.RawQuery

	// Headers
	for name, values := range req.Header {
		if len(values) > 0 {
			data["header_"+strings.ToLower(name)] = values[0]
		}
	}

	// Method
	data["method"] = req.Method

	// User Agent
	data["user_agent"] = req.UserAgent()

	return data
}

// checkRule checks a specific rule against request data
func (wm *WAFManager) checkRule(rule WAFFRule, requestData map[string]string) WAFResult {
	result := WAFResult{
		Rule:      &rule,
		RiskScore: wm.calculateRiskScore(rule.Severity),
	}

	// Check each data field
	for field, value := range requestData {
		if rule.Pattern.MatchString(value) {
			result.Blocked = true
			result.Reason = fmt.Sprintf("Rule %s matched in %s field", rule.ID, field)
			return result
		}
	}

	return result
}

// calculateRiskScore calculates risk score based on severity
func (wm *WAFManager) calculateRiskScore(severity string) float64 {
	switch severity {
	case "low":
		return 0.3
	case "medium":
		return 0.5
	case "high":
		return 0.7
	case "critical":
		return 0.9
	default:
		return 0.5
	}
}

// logWAFEvent logs WAF events
func (wm *WAFManager) logWAFEvent(rule WAFFRule, req *http.Request, securityCtx *SecurityContext) {
	wm.logger.Warn("WAF rule triggered",
		zap.String("rule_id", rule.ID),
		zap.String("rule_name", rule.Name),
		zap.String("rule_type", rule.Type),
		zap.String("severity", rule.Severity),
		zap.String("action", rule.Action),
		zap.String("client_ip", securityCtx.ClientIP),
		zap.String("user_agent", securityCtx.UserAgent),
		zap.String("method", req.Method),
		zap.String("path", req.URL.Path),
		zap.String("query", req.URL.RawQuery))
}

// AddRule adds a new WAF rule
func (wm *WAFManager) AddRule(rule WAFFRule) error {
	// Compile regex pattern
	pattern, err := regexp.Compile(rule.Pattern.String())
	if err != nil {
		return fmt.Errorf("invalid regex pattern in rule %s: %w", rule.ID, err)
	}

	rule.Pattern = pattern
	wm.rules = append(wm.rules, rule)

	wm.logger.Info("WAF rule added",
		zap.String("rule_id", rule.ID),
		zap.String("rule_name", rule.Name))

	return nil
}

// RemoveRule removes a WAF rule
func (wm *WAFManager) RemoveRule(ruleID string) error {
	for i, rule := range wm.rules {
		if rule.ID == ruleID {
			wm.rules = append(wm.rules[:i], wm.rules[i+1:]...)
			wm.logger.Info("WAF rule removed",
				zap.String("rule_id", ruleID))
			return nil
		}
	}

	return fmt.Errorf("rule %s not found", ruleID)
}

// GetRules returns all WAF rules
func (wm *WAFManager) GetRules() []WAFFRule {
	return wm.rules
}

// GetStats returns WAF statistics
func (wm *WAFManager) GetStats() map[string]interface{} {
	stats := make(map[string]interface{})
	stats["total_rules"] = len(wm.rules)
	stats["enabled_rules"] = 0
	stats["mode"] = wm.config.Mode

	for _, rule := range wm.rules {
		if rule.Enabled {
			stats["enabled_rules"] = stats["enabled_rules"].(int) + 1
		}
	}

	return stats
}
