package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"loadbalancer/internal/websocket"
)

func main() {
	var (
		addr = flag.String("addr", ":8081", "WebSocket server address")
		_    = flag.Bool("prod", true, "Production mode (real data only)")
	)
	flag.Parse()

	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	// Create WebSocket server
	wsServer := websocket.NewWebSocketServer(logger)

	// Start server in a goroutine
	go func() {
		logger.Info("Starting dashboard server", zap.String("addr", *addr))
		if err := wsServer.Start(*addr); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down dashboard server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Stop WebSocket server
	wsServer.Stop()

	<-ctx.Done()
	logger.Info("Dashboard server stopped")
}
