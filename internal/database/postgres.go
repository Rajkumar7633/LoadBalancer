package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

// PostgreSQL implementation for production database
type PostgreSQL struct {
	db     *sqlx.DB
	logger *zap.Logger
	config Config
}

// Config holds database configuration
type Config struct {
	Host            string        `json:"host"`
	Port            int           `json:"port"`
	User            string        `json:"user"`
	Password        string        `json:"password"`
	DBName          string        `json:"dbname"`
	SSLMode         string        `json:"sslmode"`
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time"`
}

// NewPostgreSQL creates a new PostgreSQL connection
func NewPostgreSQL(config Config, logger *zap.Logger) (*PostgreSQL, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.Host, config.Port, config.User, config.Password, config.DBName, config.SSLMode)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime)

	pg := &PostgreSQL{
		db:     db,
		logger: logger,
		config: config,
	}

	// Test connection
	if err := pg.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Initialize schema
	if err := pg.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	logger.Info("PostgreSQL connection established",
		zap.String("host", config.Host),
		zap.String("database", config.DBName))

	return pg, nil
}

// Ping tests the database connection
func (pg *PostgreSQL) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return pg.db.PingContext(ctx)
}

// Close closes the database connection
func (pg *PostgreSQL) Close() error {
	return pg.db.Close()
}

// initSchema creates the necessary tables
func (pg *PostgreSQL) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS metrics (
		id SERIAL PRIMARY KEY,
		timestamp TIMESTAMP WITH TIME ZONE NOT NULL,
		metric_name VARCHAR(100) NOT NULL,
		metric_value DOUBLE PRECISION NOT NULL,
		labels JSONB,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS backend_status (
		id SERIAL PRIMARY KEY,
		backend_id VARCHAR(100) NOT NULL,
		backend_url VARCHAR(500) NOT NULL,
		status VARCHAR(20) NOT NULL,
		connections INTEGER DEFAULT 0,
		response_time INTEGER DEFAULT 0,
		request_count BIGINT DEFAULT 0,
		error_count BIGINT DEFAULT 0,
		last_checked TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		UNIQUE(backend_id)
	);

	CREATE TABLE IF NOT EXISTS security_events (
		id SERIAL PRIMARY KEY,
		event_type VARCHAR(50) NOT NULL,
		message TEXT NOT NULL,
		severity VARCHAR(20) NOT NULL,
		client_ip INET,
		user_agent TEXT,
		request_id VARCHAR(100),
		timestamp TIMESTAMP WITH TIME ZONE NOT NULL,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS autoscaling_events (
		id SERIAL PRIMARY KEY,
		event_type VARCHAR(50) NOT NULL,
		old_scale INTEGER NOT NULL,
		new_scale INTEGER NOT NULL,
		reason TEXT,
		cpu_usage DOUBLE PRECISION,
		memory_usage DOUBLE PRECISION,
		request_rate DOUBLE PRECISION,
		timestamp TIMESTAMP WITH TIME ZONE NOT NULL,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS endpoint_stats (
		id SERIAL PRIMARY KEY,
		path VARCHAR(500) NOT NULL,
		method VARCHAR(10) NOT NULL,
		request_count BIGINT DEFAULT 0,
		avg_response_time INTEGER DEFAULT 0,
		error_rate DOUBLE PRECISION DEFAULT 0.0,
		last_minute_rps INTEGER DEFAULT 0,
		last_updated TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
		UNIQUE(path, method)
	);

	-- Create indexes for performance
	CREATE INDEX IF NOT EXISTS idx_metrics_timestamp ON metrics(timestamp);
	CREATE INDEX IF NOT EXISTS idx_metrics_name_timestamp ON metrics(metric_name, timestamp);
	CREATE INDEX IF NOT EXISTS idx_security_events_timestamp ON security_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_security_events_severity ON security_events(severity);
	CREATE INDEX IF NOT EXISTS idx_autoscaling_events_timestamp ON autoscaling_events(timestamp);
	CREATE INDEX IF NOT EXISTS idx_endpoint_stats_path ON endpoint_stats(path);
	CREATE INDEX IF NOT EXISTS idx_backend_status_last_checked ON backend_status(last_checked);

	-- Create partitioned table for high-volume metrics
	CREATE TABLE IF NOT EXISTS metrics_partitioned (
		LIKE metrics INCLUDING ALL
	) PARTITION BY RANGE (timestamp);

	-- Create partitions for current and future months
	CREATE TABLE IF NOT EXISTS metrics_2024_01 PARTITION OF metrics_partitioned
		FOR VALUES FROM ('2024-01-01') TO ('2024-02-01');
	CREATE TABLE IF NOT EXISTS metrics_2024_02 PARTITION OF metrics_partitioned
		FOR VALUES FROM ('2024-02-01') TO ('2024-03-01');
	CREATE TABLE IF NOT EXISTS metrics_2024_03 PARTITION OF metrics_partitioned
		FOR VALUES FROM ('2024-03-01') TO ('2024-04-01');
	`

	_, err := pg.db.Exec(schema)
	return err
}

// StoreMetric stores a metric value
func (pg *PostgreSQL) StoreMetric(ctx context.Context, name string, value float64, labels map[string]interface{}) error {
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	query := `
		INSERT INTO metrics (timestamp, metric_name, metric_value, labels)
		VALUES ($1, $2, $3, $4)
	`

	_, err = pg.db.ExecContext(ctx, query, time.Now(), name, value, labelsJSON)
	if err != nil {
		return fmt.Errorf("failed to store metric: %w", err)
	}

	return nil
}

// GetMetrics retrieves metrics within a time range
func (pg *PostgreSQL) GetMetrics(ctx context.Context, name string, start, end time.Time) ([]Metric, error) {
	query := `
		SELECT timestamp, metric_value, labels
		FROM metrics
		WHERE metric_name = $1 AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp
	`

	rows, err := pg.db.QueryxContext(ctx, query, name, start, end)
	if err != nil {
		return nil, fmt.Errorf("failed to query metrics: %w", err)
	}
	defer rows.Close()

	var metrics []Metric
	for rows.Next() {
		var metric Metric
		var labelsJSON []byte

		if err := rows.Scan(&metric.Timestamp, &metric.Value, &labelsJSON); err != nil {
			return nil, fmt.Errorf("failed to scan metric: %w", err)
		}

		if err := json.Unmarshal(labelsJSON, &metric.Labels); err != nil {
			return nil, fmt.Errorf("failed to unmarshal labels: %w", err)
		}

		metrics = append(metrics, metric)
	}

	return metrics, nil
}

// Metric represents a stored metric
type Metric struct {
	Timestamp time.Time              `json:"timestamp"`
	Value     float64                `json:"value"`
	Labels    map[string]interface{} `json:"labels"`
}

// UpdateBackendStatus updates backend status
func (pg *PostgreSQL) UpdateBackendStatus(ctx context.Context, backendID, url, status string, connections, responseTime int, requestCount, errorCount int64) error {
	query := `
		INSERT INTO backend_status (backend_id, backend_url, status, connections, response_time, request_count, error_count, last_checked)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (backend_id) 
		DO UPDATE SET 
			status = EXCLUDED.status,
			connections = EXCLUDED.connections,
			response_time = EXCLUDED.response_time,
			request_count = EXCLUDED.request_count,
			error_count = EXCLUDED.error_count,
			last_checked = EXCLUDED.last_checked
	`

	_, err := pg.db.ExecContext(ctx, query, backendID, url, status, connections, responseTime, requestCount, errorCount, time.Now())
	if err != nil {
		return fmt.Errorf("failed to update backend status: %w", err)
	}

	return nil
}

// GetBackendStatus retrieves all backend statuses
func (pg *PostgreSQL) GetBackendStatus(ctx context.Context) ([]BackendStatus, error) {
	query := `
		SELECT backend_id, backend_url, status, connections, response_time, request_count, error_count, last_checked
		FROM backend_status
		ORDER BY last_checked DESC
	`

	rows, err := pg.db.QueryxContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query backend status: %w", err)
	}
	defer rows.Close()

	var statuses []BackendStatus
	for rows.Next() {
		var status BackendStatus
		if err := rows.Scan(&status.ID, &status.URL, &status.Status, &status.Connections, &status.ResponseTime, &status.RequestCount, &status.ErrorCount, &status.LastChecked); err != nil {
			return nil, fmt.Errorf("failed to scan backend status: %w", err)
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

// BackendStatus represents backend status
type BackendStatus struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Status       string    `json:"status"`
	Connections  int       `json:"connections"`
	ResponseTime int       `json:"response_time"`
	RequestCount int64     `json:"request_count"`
	ErrorCount   int64     `json:"error_count"`
	LastChecked  time.Time `json:"last_checked"`
}

// StoreSecurityEvent stores a security event
func (pg *PostgreSQL) StoreSecurityEvent(ctx context.Context, eventType, message, severity, clientIP, userAgent, requestID string) error {
	query := `
		INSERT INTO security_events (event_type, message, severity, client_ip, user_agent, request_id, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := pg.db.ExecContext(ctx, query, eventType, message, severity, clientIP, userAgent, requestID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to store security event: %w", err)
	}

	return nil
}

// GetRecentSecurityEvents retrieves recent security events
func (pg *PostgreSQL) GetRecentSecurityEvents(ctx context.Context, limit int) ([]SecurityEvent, error) {
	query := `
		SELECT event_type, message, severity, client_ip, user_agent, request_id, timestamp
		FROM security_events
		ORDER BY timestamp DESC
		LIMIT $1
	`

	rows, err := pg.db.QueryxContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query security events: %w", err)
	}
	defer rows.Close()

	var events []SecurityEvent
	for rows.Next() {
		var event SecurityEvent
		if err := rows.Scan(&event.Type, &event.Message, &event.Severity, &event.ClientIP, &event.UserAgent, &event.RequestID, &event.Timestamp); err != nil {
			return nil, fmt.Errorf("failed to scan security event: %w", err)
		}
		events = append(events, event)
	}

	return events, nil
}

// SecurityEvent represents a security event
type SecurityEvent struct {
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Severity  string    `json:"severity"`
	ClientIP  string    `json:"client_ip"`
	UserAgent string    `json:"user_agent"`
	RequestID string    `json:"request_id"`
	Timestamp time.Time `json:"timestamp"`
}

// StoreAutoscalingEvent stores an autoscaling event
func (pg *PostgreSQL) StoreAutoscalingEvent(ctx context.Context, eventType string, oldScale, newScale int, reason string, cpuUsage, memoryUsage, requestRate float64) error {
	query := `
		INSERT INTO autoscaling_events (event_type, old_scale, new_scale, reason, cpu_usage, memory_usage, request_rate, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	_, err := pg.db.ExecContext(ctx, query, eventType, oldScale, newScale, reason, cpuUsage, memoryUsage, requestRate, time.Now())
	if err != nil {
		return fmt.Errorf("failed to store autoscaling event: %w", err)
	}

	return nil
}

// UpdateEndpointStats updates endpoint statistics
func (pg *PostgreSQL) UpdateEndpointStats(ctx context.Context, path, method string, requestCount int64, avgResponseTime int, errorRate, lastMinuteRPS float64) error {
	query := `
		INSERT INTO endpoint_stats (path, method, request_count, avg_response_time, error_rate, last_minute_rps, last_updated)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (path, method)
		DO UPDATE SET
			request_count = EXCLUDED.request_count,
			avg_response_time = EXCLUDED.avg_response_time,
			error_rate = EXCLUDED.error_rate,
			last_minute_rps = EXCLUDED.last_minute_rps,
			last_updated = EXCLUDED.last_updated
	`

	_, err := pg.db.ExecContext(ctx, query, path, method, requestCount, avgResponseTime, errorRate, lastMinuteRPS, time.Now())
	if err != nil {
		return fmt.Errorf("failed to update endpoint stats: %w", err)
	}

	return nil
}

// GetTopEndpoints retrieves top endpoints by request count
func (pg *PostgreSQL) GetTopEndpoints(ctx context.Context, limit int) ([]EndpointStats, error) {
	query := `
		SELECT path, method, request_count, avg_response_time, error_rate, last_minute_rps, last_updated
		FROM endpoint_stats
		ORDER BY request_count DESC
		LIMIT $1
	`

	rows, err := pg.db.QueryxContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query endpoint stats: %w", err)
	}
	defer rows.Close()

	var stats []EndpointStats
	for rows.Next() {
		var stat EndpointStats
		if err := rows.Scan(&stat.Path, &stat.Method, &stat.Requests, &stat.AvgResponseTime, &stat.ErrorRate, &stat.LastMinuteRPS, &stat.LastUpdated); err != nil {
			return nil, fmt.Errorf("failed to scan endpoint stats: %w", err)
		}
		stats = append(stats, stat)
	}

	return stats, nil
}

// EndpointStats represents endpoint statistics
type EndpointStats struct {
	Path            string    `json:"path"`
	Method          string    `json:"method"`
	Requests        int64     `json:"requests"`
	AvgResponseTime int       `json:"avg_response_time"`
	ErrorRate       float64   `json:"error_rate"`
	LastMinuteRPS   float64   `json:"last_minute_rps"`
	LastUpdated     time.Time `json:"last_updated"`
}

// CleanupOldData removes old data to prevent database bloat
func (pg *PostgreSQL) CleanupOldData(ctx context.Context, retentionDays int) error {
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	// Clean up old metrics
	_, err := pg.db.ExecContext(ctx, "DELETE FROM metrics WHERE timestamp < $1", cutoffTime)
	if err != nil {
		return fmt.Errorf("failed to cleanup old metrics: %w", err)
	}

	// Clean up old security events
	_, err = pg.db.ExecContext(ctx, "DELETE FROM security_events WHERE timestamp < $1", cutoffTime)
	if err != nil {
		return fmt.Errorf("failed to cleanup old security events: %w", err)
	}

	pg.logger.Info("Cleaned up old data", zap.Time("cutoff", cutoffTime))
	return nil
}

// GetDatabaseStats returns database statistics
func (pg *PostgreSQL) GetDatabaseStats(ctx context.Context) (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Get connection pool stats
	dbStats := pg.db.Stats()
	stats["max_open_connections"] = dbStats.MaxOpenConnections
	stats["open_connections"] = dbStats.OpenConnections
	stats["in_use"] = dbStats.InUse
	stats["idle"] = dbStats.Idle

	// Get table sizes with proper error handling and existence checks
	tables := []string{"metrics", "backend_status", "security_events", "autoscaling_events", "endpoint_stats"}
	stats["table_errors"] = []string{}

	for _, table := range tables {
		// First check if table exists
		var exists bool
		err := pg.db.GetContext(ctx, &exists,
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)", table)

		if err != nil {
			pg.logger.Warn("Failed to check table existence",
				zap.String("table", table),
				zap.Error(err))
			stats["table_errors"] = append(stats["table_errors"].([]string),
				fmt.Sprintf("%s: check existence failed - %v", table, err))
			continue
		}

		if !exists {
			pg.logger.Debug("Table does not exist", zap.String("table", table))
			stats["table_errors"] = append(stats["table_errors"].([]string),
				fmt.Sprintf("%s: table does not exist", table))
			continue
		}

		// Now get table size
		var size int64
		err = pg.db.GetContext(ctx, &size, "SELECT pg_total_relation_size($1)", table)

		if err != nil {
			pg.logger.Warn("Failed to get table size",
				zap.String("table", table),
				zap.Error(err))
			stats["table_errors"] = append(stats["table_errors"].([]string),
				fmt.Sprintf("%s: size query failed - %v", table, err))
			continue
		}

		stats[table+"_size_bytes"] = size
	}

	return stats, nil
}
