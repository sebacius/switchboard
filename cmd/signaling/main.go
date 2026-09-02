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
	"github.com/sebas/switchboard/internal/logger/siplog"
	"github.com/sebas/switchboard/internal/signaling/app"
	"github.com/sebas/switchboard/internal/signaling/config"
	"github.com/sebas/switchboard/internal/signaling/validate"
)

func main() {
	// Subcommands are dispatched before flag parsing, because the global flag
	// set belongs to the server and a subcommand needs its own.
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		os.Exit(validate.Run(os.Args[2:], os.Stdout))
	}

	// Load configuration. A bad value is reported here, before the banner prints
	// values it could not resolve.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		// 2 is the flag-usage convention; the logger is not up yet.
		os.Exit(2)
	}

	// Print startup banner
	banner.Print("SIGNALING SERVER", []banner.ConfigLine{
		{Label: "SIP", Value: fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)},
		// The REST API the admin UI reads. Announced here rather than only in a
		// log line after the banner: this block is where an operator looks to
		// find out what is listening, and the UI is unusable without this port.
		{Label: "API", Value: fmt.Sprintf("http://%s:%d", cfg.BindAddr, cfg.APIPort)},
		{Label: "Advertise", Value: cfg.AdvertiseAddr},
		{Label: "RTP Manager", Value: strings.Join(cfg.RTPManagerAddrs, ", ")},
		{Label: "Policy", Value: cfg.PolicyPath},
		{Label: "Tenants", Value: cfg.TenantsPath},
		{Label: "Routing", Value: cfg.RoutingPath},
		{Label: "Log Level", Value: cfg.LogLevel},
		{Label: "SIP Log Level", Value: cfg.SIPLogLevel},
	})

	// Initialize logging before anything that captures slog.Default or the
	// zerolog global. app.NewServer does both.
	logger.InitLogger(os.Stdout)
	format, formatErr := logger.ParseFormat(cfg.LogFormat)
	logger.SetFormat(format)
	levelErr := logger.SetLevel(cfg.LogLevel)

	// sipgo's transport and transaction layers copy the zerolog global when the
	// user agent is built, so this has to happen before app.NewServer.
	sipErr := siplog.Configure(cfg.SIPLogLevel)

	for _, err := range []error{formatErr, levelErr, sipErr} {
		if err != nil {
			slog.Warn("[CONFIG] " + err.Error())
		}
	}

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
