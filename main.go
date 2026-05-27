package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"loadbalancer/internal/config"
	"loadbalancer/internal/loadbalancer"
	"loadbalancer/internal/logger"
	"loadbalancer/internal/metrics"
)

func main() {
	// Initialize structured logger
	log := logger.NewLogger()
	log.Info("Starting Advanced Load Balancer")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("Failed to load configuration", "error", err)
	}

	// Initialize metrics collector
	metricsCollector := metrics.NewCollector()
	go metricsCollector.StartServer(cfg.Metrics.Port)

	// Create load balancer instance
	lb, err := loadbalancer.New(cfg, log, metricsCollector)
	if err != nil {
		log.Fatal("Failed to create load balancer", "error", err)
	}

	// Start health checks
	go lb.StartHealthChecks(context.Background())

	// Setup HTTP server with TLS
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      lb,
		ReadTimeout:  time.Duration(cfg.RequestTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.RequestTimeout) * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Configure TLS if certificates are provided
	if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
		server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
		}
	}

	// Start server in goroutine
	go func() {
		log.Info("Load balancer starting", "port", cfg.Port, "tls", cfg.TLS.CertFile != "")
		var err error
		if cfg.TLS.CertFile != "" && cfg.TLS.KeyFile != "" {
			err = server.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatal("Server failed", "error", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down load balancer...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown", "error", err)
	}

	lb.Shutdown()
	log.Info("Load balancer stopped")
}
