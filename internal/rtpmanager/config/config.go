package config

import (
	"flag"
	"net"
	"os"
	"strconv"

	"github.com/sebas/switchboard/internal/rtpmanager/asr"
)

// Config holds the RTP Manager configuration
type Config struct {
	GRPCPort      int
	GRPCBindAddr  string
	AdvertiseAddr string // Address to advertise in SDP
	RTPPortMin    int
	RTPPortMax    int
	AudioBasePath string
	LogLevel      string
	TTSServerURL  string // TTS server URL (e.g., "http://localhost:8000")
	ASRServerURL  string // ASR/Whisper server URL (e.g., "http://localhost:9000")
	ASRModel      string // Transcription model id; required by the OpenAI /v1/audio/transcriptions contract
}

// Load loads configuration from command line flags and environment variables
func Load() *Config {
	cfg := &Config{}

	flag.IntVar(&cfg.GRPCPort, "grpc-port", 9090, "gRPC server port")
	flag.StringVar(&cfg.GRPCBindAddr, "bind", "0.0.0.0", "gRPC bind address")
	flag.StringVar(&cfg.AdvertiseAddr, "advertise", "", "Address to advertise in SDP (auto-detected if not set)")
	flag.IntVar(&cfg.RTPPortMin, "rtp-port-min", 10000, "Minimum RTP port")
	flag.IntVar(&cfg.RTPPortMax, "rtp-port-max", 20000, "Maximum RTP port")
	flag.StringVar(&cfg.AudioBasePath, "audio-path", "./audio", "Audio files base path")
	flag.StringVar(&cfg.LogLevel, "loglevel", "debug", "Log level")
	flag.StringVar(&cfg.TTSServerURL, "tts-server", "http://localhost:8000", "TTS server URL")
	flag.StringVar(&cfg.ASRServerURL, "asr-server", "http://localhost:8001", "ASR/Whisper server URL")
	flag.StringVar(&cfg.ASRModel, "asr-model", asr.DefaultModel, "Transcription model id sent to the ASR server (required by the API; override for a different Whisper build)")

	flag.Parse()

	// Environment overrides
	if v := os.Getenv("GRPC_PORT"); v != "" {
		cfg.GRPCPort, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("BIND"); v != "" {
		cfg.GRPCBindAddr = v
	}
	if v := os.Getenv("ADVERTISE"); v != "" {
		cfg.AdvertiseAddr = v
	} else if cfg.AdvertiseAddr == "" {
		cfg.AdvertiseAddr = getPrimaryInterfaceIP()
	}
	if v := os.Getenv("RTP_PORT_MIN"); v != "" {
		cfg.RTPPortMin, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("RTP_PORT_MAX"); v != "" {
		cfg.RTPPortMax, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("AUDIO_PATH"); v != "" {
		cfg.AudioBasePath = v
	}
	if v := os.Getenv("LOGLEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("TTS_SERVER"); v != "" {
		cfg.TTSServerURL = v
	}
	if v := os.Getenv("ASR_MODEL"); v != "" {
		cfg.ASRModel = v
	}
	if v := os.Getenv("ASR_SERVER"); v != "" {
		cfg.ASRServerURL = v
	}

	return cfg
}

// getPrimaryInterfaceIP detects the primary network interface IP address
func getPrimaryInterfaceIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return "127.0.0.1"
}
