package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v2"
)

// ConfigReloader manages configuration hot-reload functionality
type ConfigReloader struct {
	configPath      string
	config          interface{}
	reloadCallbacks map[string]ReloadCallback
	watcher         *FileWatcher
	logger          *zap.Logger
	mu              sync.RWMutex

	// Reload state
	lastReload  time.Time
	reloadCount int64
	isReloading bool
	reloadChan  chan ReloadEvent

	// Configuration
	reloadInterval time.Duration
	debounceDelay  time.Duration
	maxRetries     int
}

// ReloadCallback is called when configuration is reloaded
type ReloadCallback func(oldConfig, newConfig interface{}) error

// ReloadEvent represents a configuration reload event
type ReloadEvent struct {
	Timestamp time.Time              `json:"timestamp"`
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
	Changes   map[string]interface{} `json:"changes,omitempty"`
	Duration  time.Duration          `json:"duration"`
}

// FileWatcher watches for file changes
type FileWatcher struct {
	path    string
	changes chan string
	stop    chan bool
	logger  *zap.Logger
}

// ConfigReloaderConfig contains configuration for the reloader
type ConfigReloaderConfig struct {
	ConfigPath     string        `json:"config_path"`
	ReloadInterval time.Duration `json:"reload_interval"`
	DebounceDelay  time.Duration `json:"debounce_delay"`
	MaxRetries     int           `json:"max_retries"`
	Logger         *zap.Logger   `json:"-"`
}

// NewConfigReloader creates a new configuration reloader
func NewConfigReloader(config ConfigReloaderConfig) *ConfigReloader {
	if config.ReloadInterval <= 0 {
		config.ReloadInterval = 30 * time.Second
	}
	if config.DebounceDelay <= 0 {
		config.DebounceDelay = 1 * time.Second
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}

	return &ConfigReloader{
		configPath:      config.ConfigPath,
		reloadCallbacks: make(map[string]ReloadCallback),
		reloadChan:      make(chan ReloadEvent, 10),
		logger:          config.Logger,
		reloadInterval:  config.ReloadInterval,
		debounceDelay:   config.DebounceDelay,
		maxRetries:      config.MaxRetries,
	}
}

// Start starts the configuration reloader
func (cr *ConfigReloader) Start(ctx context.Context) error {
	cr.logger.Info("Starting configuration reloader",
		zap.String("config_path", cr.configPath))

	// Load initial configuration
	if err := cr.loadConfig(); err != nil {
		return fmt.Errorf("failed to load initial config: %w", err)
	}

	// Start file watcher
	cr.watcher = NewFileWatcher(cr.configPath, cr.logger)
	go cr.watcher.Start(ctx)

	// Start reload processor
	go cr.processReloads(ctx)

	// Start periodic checker
	go cr.periodicCheck(ctx)

	return nil
}

// Stop stops the configuration reloader
func (cr *ConfigReloader) Stop() {
	if cr.watcher != nil {
		cr.watcher.Stop()
	}
	close(cr.reloadChan)
}

// RegisterCallback registers a reload callback
func (cr *ConfigReloader) RegisterCallback(name string, callback ReloadCallback) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	cr.reloadCallbacks[name] = callback
}

// UnregisterCallback unregisters a reload callback
func (cr *ConfigReloader) UnregisterCallback(name string) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	delete(cr.reloadCallbacks, name)
}

// processReloads processes reload events
func (cr *ConfigReloader) processReloads(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-cr.watcher.changes:
			cr.triggerReload("file_change")
		case <-time.After(cr.reloadInterval):
			cr.triggerReload("periodic_check")
		}
	}
}

// triggerReload triggers a configuration reload
func (cr *ConfigReloader) triggerReload(reason string) {
	cr.mu.Lock()
	if cr.isReloading {
		cr.mu.Unlock()
		return
	}
	cr.isReloading = true
	cr.mu.Unlock()

	defer func() {
		cr.mu.Lock()
		cr.isReloading = false
		cr.mu.Unlock()
	}()

	cr.logger.Info("Triggering configuration reload", zap.String("reason", reason))

	event := ReloadEvent{
		Timestamp: time.Now(),
	}

	start := time.Now()

	// Debounce rapid changes
	time.Sleep(cr.debounceDelay)

	// Load new configuration
	newConfig, err := cr.readConfig()
	if err != nil {
		event.Success = false
		event.Error = err.Error()
		event.Duration = time.Since(start)
		cr.reloadChan <- event
		cr.logger.Error("Failed to reload configuration", zap.Error(err))
		return
	}

	// Compare with current config
	oldConfig := cr.config
	if cr.configEqual(oldConfig, newConfig) {
		cr.logger.Debug("Configuration unchanged, skipping reload")
		return
	}

	// Apply new configuration
	if err := cr.applyNewConfig(oldConfig, newConfig); err != nil {
		event.Success = false
		event.Error = err.Error()
		event.Duration = time.Since(start)
		cr.reloadChan <- event
		cr.logger.Error("Failed to apply new configuration", zap.Error(err))
		return
	}

	// Update current config
	cr.config = newConfig
	cr.lastReload = time.Now()
	cr.reloadCount++

	event.Success = true
	event.Duration = time.Since(start)
	event.Changes = cr.detectChanges(oldConfig, newConfig)
	cr.reloadChan <- event

	cr.logger.Info("Configuration reloaded successfully",
		zap.Duration("duration", event.Duration),
		zap.Int64("reload_count", cr.reloadCount))
}

// loadConfig loads the initial configuration
func (cr *ConfigReloader) loadConfig() error {
	config, err := cr.readConfig()
	if err != nil {
		return err
	}
	cr.config = config
	return nil
}

// readConfig reads configuration from file
func (cr *ConfigReloader) readConfig() (interface{}, error) {
	data, err := os.ReadFile(cr.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Determine file format
	ext := filepath.Ext(cr.configPath)

	var config interface{}

	switch ext {
	case ".json":
		config = make(map[string]interface{})
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to parse JSON config: %w", err)
		}
	case ".yaml", ".yml":
		config = make(map[string]interface{})
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to parse YAML config: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config format: %s", ext)
	}

	return config, nil
}

// configEqual compares two configurations
func (cr *ConfigReloader) configEqual(old, new interface{}) bool {
	// Simple comparison - in production, you'd want more sophisticated comparison
	oldBytes, _ := json.Marshal(old)
	newBytes, _ := json.Marshal(new)
	return string(oldBytes) == string(newBytes)
}

// applyNewConfig applies new configuration
func (cr *ConfigReloader) applyNewConfig(oldConfig, newConfig interface{}) error {
	cr.mu.RLock()
	callbacks := make(map[string]ReloadCallback)
	for name, callback := range cr.reloadCallbacks {
		callbacks[name] = callback
	}
	cr.mu.RUnlock()

	// Call all callbacks
	for name, callback := range callbacks {
		if err := callback(oldConfig, newConfig); err != nil {
			cr.logger.Error("Reload callback failed",
				zap.String("callback", name),
				zap.Error(err))
			return fmt.Errorf("callback %s failed: %w", name, err)
		}
	}

	return nil
}

// detectChanges detects changes between configurations
func (cr *ConfigReloader) detectChanges(old, new interface{}) map[string]interface{} {
	changes := make(map[string]interface{})

	// Simple change detection - in production, you'd want more sophisticated diff
	oldMap, ok := old.(map[string]interface{})
	if !ok {
		return changes
	}

	newMap, ok := new.(map[string]interface{})
	if !ok {
		return changes
	}

	for key, newValue := range newMap {
		if oldValue, exists := oldMap[key]; !exists || oldValue != newValue {
			changes[key] = newValue
		}
	}

	return changes
}

// periodicCheck performs periodic configuration checks
func (cr *ConfigReloader) periodicCheck(ctx context.Context) {
	ticker := time.NewTicker(cr.reloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cr.triggerReload("periodic_check")
		}
	}
}

// GetReloadStats returns reload statistics
func (cr *ConfigReloader) GetReloadStats() ConfigReloadStats {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	return ConfigReloadStats{
		LastReload:  cr.lastReload,
		ReloadCount: cr.reloadCount,
		IsReloading: cr.isReloading,
		ConfigPath:  cr.configPath,
	}
}

// GetReloadEvents returns recent reload events
func (cr *ConfigReloader) GetReloadEvents() []ReloadEvent {
	// This would maintain a buffer of recent events
	// For simplicity, returning empty slice
	return []ReloadEvent{}
}

// ForceReload forces a configuration reload
func (cr *ConfigReloader) ForceReload() error {
	cr.triggerReload("manual_force")
	return nil
}

// ConfigReloadStats contains configuration reload statistics
type ConfigReloadStats struct {
	LastReload  time.Time `json:"last_reload"`
	ReloadCount int64     `json:"reload_count"`
	IsReloading bool      `json:"is_reloading"`
	ConfigPath  string    `json:"config_path"`
}

// NewFileWatcher creates a new file watcher
func NewFileWatcher(path string, logger *zap.Logger) *FileWatcher {
	return &FileWatcher{
		path:    path,
		changes: make(chan string, 10),
		stop:    make(chan bool),
		logger:  logger,
	}
}

// Start starts the file watcher
func (fw *FileWatcher) Start(ctx context.Context) {
	fw.logger.Info("Starting file watcher", zap.String("path", fw.path))

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastModTime time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-fw.stop:
			return
		case <-ticker.C:
			info, err := os.Stat(fw.path)
			if err != nil {
				fw.logger.Error("Failed to stat file", zap.Error(err))
				continue
			}

			if info.ModTime().After(lastModTime) {
				lastModTime = info.ModTime()
				select {
				case fw.changes <- "file_modified":
				default:
				}
			}
		}
	}
}

// Stop stops the file watcher
func (fw *FileWatcher) Stop() {
	close(fw.stop)
}

// ConfigValidator validates configuration
type ConfigValidator struct {
	rules  []ValidationRule
	logger *zap.Logger
}

// ValidationRule represents a validation rule
type ValidationRule struct {
	Name     string
	Validate func(interface{}) error
}

// NewConfigValidator creates a new configuration validator
func NewConfigValidator(logger *zap.Logger) *ConfigValidator {
	return &ConfigValidator{
		rules:  make([]ValidationRule, 0),
		logger: logger,
	}
}

// AddRule adds a validation rule
func (cv *ConfigValidator) AddRule(rule ValidationRule) {
	cv.rules = append(cv.rules, rule)
}

// Validate validates the configuration
func (cv *ConfigValidator) Validate(config interface{}) error {
	for _, rule := range cv.rules {
		if err := rule.Validate(config); err != nil {
			return fmt.Errorf("validation rule %s failed: %w", rule.Name, err)
		}
	}
	return nil
}

// ConfigManager manages configuration with hot-reload
type ConfigManager struct {
	reloader  *ConfigReloader
	validator *ConfigValidator
	logger    *zap.Logger
	config    interface{}
	mu        sync.RWMutex
}

// NewConfigManager creates a new configuration manager
func NewConfigManager(configPath string, logger *zap.Logger) *ConfigManager {
	reloaderConfig := ConfigReloaderConfig{
		ConfigPath: configPath,
		Logger:     logger,
	}

	return &ConfigManager{
		reloader:  NewConfigReloader(reloaderConfig),
		validator: NewConfigValidator(logger),
		logger:    logger,
	}
}

// Start starts the configuration manager
func (cm *ConfigManager) Start(ctx context.Context) error {
	return cm.reloader.Start(ctx)
}

// Stop stops the configuration manager
func (cm *ConfigManager) Stop() {
	cm.reloader.Stop()
}

// GetConfig returns the current configuration
func (cm *ConfigManager) GetConfig() interface{} {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config
}

// UpdateConfig updates the configuration
func (cm *ConfigManager) UpdateConfig(config interface{}) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Validate new config
	if err := cm.validator.Validate(config); err != nil {
		return err
	}

	cm.config = config
	return nil
}

// RegisterReloadCallback registers a reload callback
func (cm *ConfigManager) RegisterReloadCallback(name string, callback ReloadCallback) {
	cm.reloader.RegisterCallback(name, func(oldConfig, newConfig interface{}) error {
		err := callback(oldConfig, newConfig)
		if err == nil {
			cm.mu.Lock()
			cm.config = newConfig
			cm.mu.Unlock()
		}
		return err
	})
}

// AddValidationRule adds a validation rule
func (cm *ConfigManager) AddValidationRule(rule ValidationRule) {
	cm.validator.AddRule(rule)
}
