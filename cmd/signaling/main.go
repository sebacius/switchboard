package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sebas/switchboard/internal/logger"
	"github.com/sebas/switchboard/services/signaling/app"
	"github.com/sebas/switchboard/services/signaling/config"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Initialize logger
	logger.InitLogger(os.Stdout)

	// Create server
	swboard, err := app.NewServer(cfg)
	if err != nil {
		slog.Error("Failed to create signaling server", "error", err)
		os.Exit(1)
	}
	defer swboard.Close()

	run(swboard, cfg)
}

func run(proxy *app.SwitchBoard, cfg *config.Config) {
	slog.Info("Starting Switchboard Signaling Server",
		"port", cfg.Port,
		"rtpmanagers", cfg.RTPManagerAddrs,
	)

	slog.Info("API available at http://0.0.0.0:8080")
	logNetworkInterfaces()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	go func() {
		if err := proxy.Start(ctx); err != nil {
			slog.Error("Server error", "error", err)
		}
	}()

	// Wait for signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	slog.Info("Received signal, shutting down", "signal", sig)
	cancel()

	time.Sleep(1 * time.Second)
}

func logNetworkInterfaces() {
	interfaces, err := net.Interfaces()
	if err != nil {
		return
	}

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			slog.Debug("Network interface", "interface", iface.Name, "ip", ip.String())
		}
	}
}
