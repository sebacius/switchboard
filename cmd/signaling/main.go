package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sebas/switchboard/internal/banner"
	"github.com/sebas/switchboard/internal/logger"
	"github.com/sebas/switchboard/internal/signaling/app"
	"github.com/sebas/switchboard/internal/signaling/config"
	"github.com/sebas/switchboard/internal/signaling/llm"
)

func main() {
	// Load configuration. A bad --llm-model is reported here, before the banner
	// prints values it could not resolve.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		// 2 is the flag-usage convention; the logger is not up yet.
		os.Exit(2)
	}

	// Print startup banner
	banner.Print("SIGNALING SERVER", []banner.ConfigLine{
		{Label: "Listen", Value: fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)},
		{Label: "Advertise", Value: cfg.AdvertiseAddr},
		{Label: "RTP Manager", Value: strings.Join(cfg.RTPManagerAddrs, ", ")},
		{Label: "LLM Provider", Value: string(cfg.LLMProvider)},
		{Label: "LLM Server", Value: cfg.LLMServerURL},
		{Label: "Supervisor Model", Value: cfg.LLMModel},
		{Label: "LLM Auth", Value: llmAuthStatus(cfg.LLMProvider)},
		{Label: "Policy", Value: cfg.PolicyPath},
		{Label: "Tenants", Value: cfg.TenantsPath},
		{Label: "Log Level", Value: cfg.LogLevel},
	})

	// Initialize logger
	logger.InitLogger(os.Stdout)

	// Create server
	swboard, err := app.NewServer(cfg)
	if err != nil {
		slog.Error("Failed to create signaling server", "error", err)
		os.Exit(1)
	}
	defer func() { _ = swboard.Close() }()

	run(swboard, cfg)
}

func run(swboard *app.SwitchBoard, cfg *config.Config) {
	slog.Info("Starting Switchboard Signaling Server",
		"port", cfg.Port,
		"rtpmanagers", cfg.RTPManagerAddrs,
	)

	slog.Info(fmt.Sprintf("API available at http://%s:%d", cfg.BindAddr, cfg.APIPort))
	logNetworkInterfaces()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	go func() {
		if err := swboard.Start(ctx); err != nil {
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

// llmAuthStatus reports whether a credential is available, never what it is.
// It answers the first question anyone debugging a 401 asks, and leaks nothing:
// not the key, not a prefix of it, not its length.
func llmAuthStatus(p llm.Provider) string {
	if p != llm.ProviderOpenAI {
		return "not required"
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return "OPENAI_API_KEY set"
	}
	return "none (unauthenticated)"
}
