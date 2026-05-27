package security

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// AuditLogger handles security audit logging
type AuditLogger struct {
	config AuditLoggerConfig
	logger *zap.Logger
	file   *os.File
	mu     sync.Mutex
}

// AuditLoggerConfig contains audit logger configuration
type AuditLoggerConfig struct {
	LogPath       string      `json:"log_path"`
	RetentionDays int         `json:"retention_days"`
	Logger        *zap.Logger `json:"-"`
}

// AuditEvent represents a security audit event
type AuditEvent struct {
	Timestamp   time.Time              `json:"timestamp"`
	Event       string                 `json:"event"`
	ClientIP    string                 `json:"client_ip"`
	UserAgent   string                 `json:"user_agent"`
	RequestID   string                 `json:"request_id"`
	UserID      string                 `json:"user_id,omitempty"`
	Username    string                 `json:"username,omitempty"`
	Blocked     bool                   `json:"blocked"`
	BlockReason string                 `json:"block_reason,omitempty"`
	RiskScore   float64                `json:"risk_score"`
	Error       string                 `json:"error,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// NewAuditLogger creates a new audit logger
func NewAuditLogger(config AuditLoggerConfig) (*AuditLogger, error) {
	al := &AuditLogger{
		config: config,
		logger: config.Logger,
	}

	// Open audit log file
	if err := al.openLogFile(); err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}

	// Start cleanup goroutine
	go al.startCleanup()

	al.logger.Info("Audit logger initialized",
		zap.String("log_path", config.LogPath),
		zap.Int("retention_days", config.RetentionDays))

	return al, nil
}

// openLogFile opens the audit log file
func (al *AuditLogger) openLogFile() error {
	if al.config.LogPath == "" {
		return fmt.Errorf("audit log path not specified")
	}

	file, err := os.OpenFile(al.config.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open audit log file: %w", err)
	}

	al.file = file
	return nil
}

// LogEvent logs a security audit event
func (al *AuditLogger) LogEvent(event string, securityCtx *SecurityContext, err error) {
	al.mu.Lock()
	defer al.mu.Unlock()

	auditEvent := AuditEvent{
		Timestamp:   time.Now(),
		Event:       event,
		ClientIP:    securityCtx.ClientIP,
		UserAgent:   securityCtx.UserAgent,
		RequestID:   securityCtx.RequestID,
		Blocked:     securityCtx.Blocked,
		BlockReason: securityCtx.BlockReason,
		RiskScore:   securityCtx.RiskScore,
		Metadata:    securityCtx.Metadata,
	}

	// Add user information if available
	if securityCtx.Authenticated {
		auditEvent.UserID = securityCtx.UserID
		if username, exists := securityCtx.Metadata["username"]; exists {
			auditEvent.Username = username.(string)
		}
	}

	// Add error information if available
	if err != nil {
		auditEvent.Error = err.Error()
	}

	// Write to audit log file
	if al.file != nil {
		jsonData, jsonErr := json.Marshal(auditEvent)
		if jsonErr == nil {
			al.file.WriteString(string(jsonData) + "\n")
			al.file.Sync()
		}
	}

	// Also log to structured logger
	al.logger.Info("Security audit event",
		zap.String("event", event),
		zap.String("client_ip", securityCtx.ClientIP),
		zap.String("request_id", securityCtx.RequestID),
		zap.Bool("blocked", securityCtx.Blocked),
		zap.String("block_reason", securityCtx.BlockReason),
		zap.Float64("risk_score", securityCtx.RiskScore),
		zap.String("user_id", auditEvent.UserID),
		zap.String("username", auditEvent.Username),
		zap.Error(err))
}

// startCleanup starts the cleanup process for old audit logs
func (al *AuditLogger) startCleanup() {
	if al.config.RetentionDays <= 0 {
		return // No cleanup if retention is not set
	}

	ticker := time.NewTicker(24 * time.Hour) // Run daily
	defer ticker.Stop()

	for range ticker.C {
		al.cleanupOldLogs()
	}
}

// cleanupOldLogs removes old audit log entries
func (al *AuditLogger) cleanupOldLogs() {
	al.mu.Lock()
	defer al.mu.Unlock()

	if al.file == nil {
		return
	}

	// Get file info
	fileInfo, err := al.file.Stat()
	if err != nil {
		al.logger.Error("Failed to get audit log file info", zap.Error(err))
		return
	}

	// Check if file is older than retention period
	cutoffTime := time.Now().AddDate(0, 0, -al.config.RetentionDays)
	if fileInfo.ModTime().Before(cutoffTime) {
		// Close current file
		al.file.Close()

		// Rotate file (move old file and create new one)
		backupPath := al.config.LogPath + ".old"
		if err := os.Rename(al.config.LogPath, backupPath); err != nil {
			al.logger.Error("Failed to rotate audit log file", zap.Error(err))
		} else {
			al.logger.Info("Audit log file rotated", zap.String("backup_path", backupPath))
		}

		// Open new file
		if err := al.openLogFile(); err != nil {
			al.logger.Error("Failed to open new audit log file", zap.Error(err))
		}
	}
}

// Close closes the audit logger
func (al *AuditLogger) Close() {
	al.mu.Lock()
	defer al.mu.Unlock()

	if al.file != nil {
		al.file.Close()
		al.file = nil
	}

	al.logger.Info("Audit logger closed")
}

// GetStats returns audit logger statistics
func (al *AuditLogger) GetStats() map[string]interface{} {
	al.mu.Lock()
	defer al.mu.Unlock()

	stats := make(map[string]interface{})
	stats["log_path"] = al.config.LogPath
	stats["retention_days"] = al.config.RetentionDays

	if al.file != nil {
		if fileInfo, err := al.file.Stat(); err == nil {
			stats["file_size"] = fileInfo.Size()
			stats["last_modified"] = fileInfo.ModTime()
		}
	}

	return stats
}
