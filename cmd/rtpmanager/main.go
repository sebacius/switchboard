package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"

	"github.com/sebas/switchboard/internal/banner"
	"github.com/sebas/switchboard/internal/logger"
	"github.com/sebas/switchboard/internal/rtpmanager/config"
	"github.com/sebas/switchboard/internal/rtpmanager/server"
	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Print startup banner
	banner.Print("RTP MANAGER", []banner.ConfigLine{
		{Label: "gRPC Listen", Value: fmt.Sprintf("%s:%d", cfg.GRPCBindAddr, cfg.GRPCPort)},
		{Label: "Advertise", Value: cfg.AdvertiseAddr},
		{Label: "RTP Range", Value: fmt.Sprintf("%d-%d", cfg.RTPPortMin, cfg.RTPPortMax)},
		{Label: "Audio Path", Value: cfg.AudioBasePath},
		{Label: "Log Level", Value: cfg.LogLevel},
	})

	// Initialize logger
	logger.InitLogger(os.Stdout)

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
	defer func() { _ = rtpSrv.Close() }()

	// Create gRPC server with logging interceptors and keepalive settings
	grpcServer := grpc.NewServer(
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second, // Ping client if idle for 30s
			Timeout: 10 * time.Second, // Wait 10s for ping ack
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // Minimum time between client pings
			PermitWithoutStream: true,             // Allow pings even without active streams
		}),
		grpc.UnaryInterceptor(loggingUnaryInterceptor),
		grpc.StreamInterceptor(loggingStreamInterceptor),
	)
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

// loggingUnaryInterceptor logs incoming unary RPC calls with peer info
func loggingUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	peerAddr := "unknown"
	if p, ok := peer.FromContext(ctx); ok {
		peerAddr = p.Addr.String()
	}
	slog.Debug("[gRPC] Incoming request", "method", info.FullMethod, "peer", peerAddr)
	return handler(ctx, req)
}

// loggingStreamInterceptor logs incoming streaming RPC calls with peer info
func loggingStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	peerAddr := "unknown"
	if p, ok := peer.FromContext(ss.Context()); ok {
		peerAddr = p.Addr.String()
	}
	slog.Debug("[gRPC] Incoming stream", "method", info.FullMethod, "peer", peerAddr)
	return handler(srv, ss)
}
