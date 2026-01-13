package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sebas/switchboard/internal/banner"
	"github.com/sebas/switchboard/internal/ui/config"
	"github.com/sebas/switchboard/internal/ui/server"
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

	// Format backends for display
	backendStrs := make([]string, len(cfg.Backends))
	for i, b := range cfg.Backends {
		backendStrs[i] = fmt.Sprintf("%s (%s)", b.Name, b.Address)
	}

	// Print startup banner
	banner.Print("UI SERVER", []banner.ConfigLine{
		{Label: "HTTP Listen", Value: fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)},
		{Label: "Backends", Value: strings.Join(backendStrs, ", ")},
		{Label: "Log Level", Value: cfg.LogLevel},
	})

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
