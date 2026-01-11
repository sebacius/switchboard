package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/sebas/switchboard/internal/logger"
	"github.com/sebas/switchboard/services/rtpmanager/config"
	"github.com/sebas/switchboard/services/rtpmanager/server"
	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Initialize logger
	logger.InitLogger(os.Stdout)

	slog.Info("Starting RTP Manager",
		"grpc_port", cfg.GRPCPort,
		"bind", cfg.GRPCBindAddr,
		"advertise", cfg.AdvertiseAddr,
		"rtp_ports", fmt.Sprintf("%d-%d", cfg.RTPPortMin, cfg.RTPPortMax),
		"audio_path", cfg.AudioBasePath,
	)

	// Create RTP Manager server
	srvCfg := &server.Config{
		GRPCPort:      cfg.GRPCPort,
		GRPCBindAddr:  cfg.GRPCBindAddr,
		AdvertiseAddr: cfg.AdvertiseAddr,
		RTPPortMin:    cfg.RTPPortMin,
		RTPPortMax:    cfg.RTPPortMax,
		AudioBasePath: cfg.AudioBasePath,
	}

	rtpSrv, err := server.NewServer(srvCfg)
	if err != nil {
		slog.Error("Failed to create RTP Manager server", "error", err)
		os.Exit(1)
	}
	defer rtpSrv.Close()

	// Create gRPC server
	grpcServer := grpc.NewServer()
	rtpv1.RegisterRTPManagerServiceServer(grpcServer, rtpSrv)

	// Start listening
	listenAddr := fmt.Sprintf("%s:%d", cfg.GRPCBindAddr, cfg.GRPCPort)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		slog.Error("Failed to listen", "address", listenAddr, "error", err)
		os.Exit(1)
	}

	slog.Info("gRPC server listening", "address", listenAddr)

	// Start server in background
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			slog.Error("gRPC server error", "error", err)
		}
	}()

	// Wait for signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	slog.Info("Received signal, shutting down", "signal", sig)

	// Graceful shutdown
	grpcServer.GracefulStop()
	slog.Info("RTP Manager stopped")
}
