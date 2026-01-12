package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sebas/switchboard/services/ui/config"
	"github.com/sebas/switchboard/services/ui/server"
)

func main() {
	// Set up structured logging
	logLevel := slog.LevelInfo
	if os.Getenv("UI_LOGLEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	// Load configuration
	cfg := config.Load()

	// Apply log level from config
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("Starting Switchboard UI",
		"port", cfg.Port,
		"bind", cfg.BindAddr,
		"backends", len(cfg.Backends),
	)

	// Create and start server
	srv, err := server.NewServer(cfg)
	if err != nil {
		slog.Error("Failed to create server", "error", err)
		os.Exit(1)
	}

	if err := srv.Start(); err != nil {
		slog.Error("Failed to start server", "error", err)
		os.Exit(1)
	}

	slog.Info("UI server started", "addr", cfg.BindAddr, "port", cfg.Port)

	// Wait for shutdown signal
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()

	slog.Info("Shutting down UI server...")
	if err := srv.Stop(); err != nil {
		slog.Error("Error during shutdown", "error", err)
	}

	slog.Info("UI server stopped")
}
