package config

import (
	"flag"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the signaling server configuration
type Config struct {
	// SIP settings
	Port          int
	BindAddr      string // Address to bind for listening (used by both SIP and API)
	AdvertiseAddr string // Address to advertise in SIP headers
	LogLevel      string

	// API settings
	APIPort int // HTTP API port (default 8080)

	// Supervisor settings. Every call is supervised by the LLM agent; there is
	// no dialplan.
	LLMServerURL string // Ollama server URL (e.g., "http://localhost:11434")
	LLMModel     string // Supervisor model served by Ollama (default "qwen3:8b")
	TTSVoice     string // Piper voice used for the supervisor's speech

	// PolicyPath points at the Class-of-Service / capacity JSON: per-tenant
	// channel limits, external-dial allowlists, symbolic targets, and the spend
	// circuit breaker. A missing file means the safe default posture.
	PolicyPath string

	// File management paths
	SettingsPath string // Directory containing settings.md (default "resources/config")
	TenantsPath  string // Directory containing tenant .md files (default "resources/tenants")

	// Trunk settings
	TrunkConfigPath string // Path to trunk peers JSON (list of {name,host,port,role})
	RoutesPath      string // Path to DID->tenant mapping (routes.json)

	// RTP Manager pool settings - two formats supported:
	//
	// Named format (Kubernetes/containers): explicit node IDs for stable pod identification
	//   RTPMANAGER=rtpmanager-0=localhost:9090,rtpmanager-1=localhost:9091
	RTPManagerNodes map[string]string
	//
	// Simple format (systemd/local dev): just addresses, node IDs auto-generated as node-0, node-1, etc.
	//   RTPMANAGER=localhost:9090,localhost:9091
	RTPManagerAddrs       []string
	GRPCConnectTimeout    time.Duration
	GRPCKeepaliveInterval time.Duration
	GRPCKeepaliveTimeout  time.Duration
}

// Load loads configuration from command line flags and environment variables
func Load() *Config {
	cfg := &Config{
		GRPCConnectTimeout:    10 * time.Second,
		GRPCKeepaliveInterval: 30 * time.Second,
		GRPCKeepaliveTimeout:  10 * time.Second,
	}

	// Define flags
	flag.IntVar(&cfg.Port, "port", 5060, "SIP listening port")
	flag.IntVar(&cfg.APIPort, "api-port", 8080, "HTTP API port")
	flag.StringVar(&cfg.BindAddr, "bind", "0.0.0.0", "Bind address for SIP and API")
	flag.StringVar(&cfg.AdvertiseAddr, "advertise", "", "Address to advertise in SIP headers (auto-detected if not set)")
	flag.StringVar(&cfg.LogLevel, "loglevel", "debug", "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.LLMServerURL, "llm-server", "http://localhost:11434", "Ollama LLM server URL")
	flag.StringVar(&cfg.LLMModel, "llm-model", "qwen3:8b", "Ollama model used by the call supervisor")
	flag.StringVar(&cfg.TTSVoice, "tts-voice", "", "Piper TTS voice for the supervisor (empty uses the RTP manager default)")
	flag.StringVar(&cfg.PolicyPath, "policy-config", "resources/config/policy.json", "Path to tenant Class-of-Service and channel-limit configuration")
	flag.StringVar(&cfg.SettingsPath, "settings-path", "resources/config", "Directory containing settings.md")
	flag.StringVar(&cfg.TenantsPath, "tenants-path", "resources/tenants", "Directory containing tenant .md files")
	flag.StringVar(&cfg.TrunkConfigPath, "trunk-config", "resources/config/trunk_peers.json", "Path to trunk peers configuration")
	flag.StringVar(&cfg.RoutesPath, "routes-path", "resources/config/routes.json", "Path to DID->tenant routes (routes.json)")

	var rtpManagerAddrs string
	flag.StringVar(&rtpManagerAddrs, "rtpmanager", "localhost:9090", "RTP Manager gRPC addresses (comma-separated for multiple)")

	flag.Parse()

	// Parse RTP manager addresses
	cfg.RTPManagerAddrs = parseAddressList(rtpManagerAddrs)

	// Override with environment variables if set
	if port := os.Getenv("PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			cfg.Port = p
		}
	}
	if apiPort := os.Getenv("API_PORT"); apiPort != "" {
		if p, err := strconv.Atoi(apiPort); err == nil {
			cfg.APIPort = p
		}
	}
	if bind := os.Getenv("BIND"); bind != "" {
		cfg.BindAddr = bind
	}
	if advertise := os.Getenv("ADVERTISE"); advertise != "" {
		cfg.AdvertiseAddr = advertise
	}
	// Validate and fallback to auto-detection if invalid
	if cfg.AdvertiseAddr == "" || !isValidAddress(cfg.AdvertiseAddr) {
		cfg.AdvertiseAddr = getPrimaryInterfaceIP()
	}
	if loglevel := os.Getenv("LOGLEVEL"); loglevel != "" {
		cfg.LogLevel = loglevel
	}
	if rtpmanager := os.Getenv("RTPMANAGER_ADDRS"); rtpmanager != "" {
		// Try parsing as node=addr format first
		nodeMap := parseNodeAddresses(rtpmanager)
		if len(nodeMap) > 0 {
			cfg.RTPManagerNodes = nodeMap
		} else {
			cfg.RTPManagerAddrs = parseAddressList(rtpmanager)
		}
	}
	if llmServer := os.Getenv("LLM_SERVER"); llmServer != "" {
		cfg.LLMServerURL = llmServer
	}
	if llmModel := os.Getenv("LLM_MODEL"); llmModel != "" {
		cfg.LLMModel = llmModel
	}
	if voice := os.Getenv("TTS_VOICE"); voice != "" {
		cfg.TTSVoice = voice
	}
	if policyPath := os.Getenv("POLICY_CONFIG"); policyPath != "" {
		cfg.PolicyPath = policyPath
	}
	if settingsPath := os.Getenv("SETTINGS_PATH"); settingsPath != "" {
		cfg.SettingsPath = settingsPath
	}
	if tenantsPath := os.Getenv("TENANTS_PATH"); tenantsPath != "" {
		cfg.TenantsPath = tenantsPath
	}
	if trunkConfig := os.Getenv("TRUNK_CONFIG"); trunkConfig != "" {
		cfg.TrunkConfigPath = trunkConfig
	}
	if routesPath := os.Getenv("ROUTES_PATH"); routesPath != "" {
		cfg.RoutesPath = routesPath
	}

	return cfg
}

// parseAddressList parses a comma-separated list of addresses
func parseAddressList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	addrs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			addrs = append(addrs, p)
		}
	}
	return addrs
}

// parseNodeAddresses parses a comma-separated list of nodeId=address pairs
// Returns nil if the format is not detected (no = signs found)
// Example: "rtpmanager-0=localhost:9090,rtpmanager-1=localhost:9091"
func parseNodeAddresses(s string) map[string]string {
	if s == "" || !strings.Contains(s, "=") {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make(map[string]string)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			// Not in node=addr format, return nil to fall back to legacy
			return nil
		}
		nodeID := strings.TrimSpace(kv[0])
		addr := strings.TrimSpace(kv[1])
		if nodeID != "" && addr != "" {
			result[nodeID] = addr
		}
	}
	return result
}

// isValidAddress checks if the address is a valid IP or resolvable hostname
func isValidAddress(addr string) bool {
	// Check if it's a valid IP address
	if ip := net.ParseIP(addr); ip != nil {
		return true
	}
	// Try to resolve as hostname
	if ips, err := net.LookupIP(addr); err == nil && len(ips) > 0 {
		return true
	}
	return false
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
