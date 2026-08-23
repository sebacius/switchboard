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

	// TTSVoice is the voice used for flow prompts (tts and ivr nodes) that do
	// not name one of their own.
	TTSVoice string

	// CDRPath is the append-only call record file. Empty disables recording.
	CDRPath string

	// PolicyPath points at the Class-of-Service / capacity JSON: per-tenant
	// channel limits, external-dial allowlists, symbolic targets, and the spend
	// circuit breaker. A missing file means the safe default posture.
	PolicyPath string

	// File management paths
	TenantsPath string // Directory containing per-tenant configuration files (default "resources/tenants")
	// RoutingPath is the directory holding per-tenant <tenant>.routing.json and
	// <tenant>.flows.json files. Empty means the same directory as
	// --tenants-path, which is where they belong; the flag exists for
	// deployments that mount them separately.
	RoutingPath string

	// AllowGlobalConfigWrites lets the config API write the deployment-wide
	// files (policy.json, routes.json, trunk_peers.json). Default false:
	// policy.json is the authorization boundary — it decides which tenant may
	// dial out and how much — and this API has no authentication, so making it
	// writable has to be an operator's decision rather than a side effect of
	// shipping an editor. Reading is always allowed.
	AllowGlobalConfigWrites bool

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

// Load loads configuration from command line flags and environment variables.
//
// It returns an error rather than exiting so that a bad value is reported before
// the startup banner prints resolved values it could not resolve.
func Load() (*Config, error) {
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
	flag.StringVar(&cfg.TTSVoice, "tts-voice", "alloy", "Default TTS voice for flow prompts")
	flag.StringVar(&cfg.CDRPath, "cdr-path", "",
		"Append-only JSONL call record file; empty disables recording")
	flag.StringVar(&cfg.PolicyPath, "policy-config", "resources/config/policy.json", "Path to tenant Class-of-Service and channel-limit configuration")
	flag.StringVar(&cfg.TenantsPath, "tenants-path", "resources/tenants", "Directory containing per-tenant configuration files")
	flag.StringVar(&cfg.RoutingPath, "routing-path", "", "Directory containing per-tenant <tenant>.routing.json and <tenant>.flows.json files (defaults to --tenants-path)")
	flag.StringVar(&cfg.TrunkConfigPath, "trunk-config", "resources/config/trunk_peers.json", "Path to trunk peers configuration")
	flag.StringVar(&cfg.RoutesPath, "routes-path", "resources/config/routes.json", "Path to DID->tenant routes (routes.json)")
	flag.BoolVar(&cfg.AllowGlobalConfigWrites, "allow-global-config-writes", false, "Allow the config API to WRITE policy.json, routes.json and trunk_peers.json (reading is always allowed)")

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
	if voice := os.Getenv("TTS_VOICE"); voice != "" {
		cfg.TTSVoice = voice
	}
	if cdrPath := os.Getenv("CDR_PATH"); cdrPath != "" {
		cfg.CDRPath = cdrPath
	}
	if policyPath := os.Getenv("POLICY_CONFIG"); policyPath != "" {
		cfg.PolicyPath = policyPath
	}
	if v := os.Getenv("ALLOW_GLOBAL_CONFIG_WRITES"); v != "" {
		cfg.AllowGlobalConfigWrites = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if tenantsPath := os.Getenv("TENANTS_PATH"); tenantsPath != "" {
		cfg.TenantsPath = tenantsPath
	}
	if routingPath := os.Getenv("ROUTING_PATH"); routingPath != "" {
		cfg.RoutingPath = routingPath
	}
	if trunkConfig := os.Getenv("TRUNK_CONFIG"); trunkConfig != "" {
		cfg.TrunkConfigPath = trunkConfig
	}
	if routesPath := os.Getenv("ROUTES_PATH"); routesPath != "" {
		cfg.RoutesPath = routesPath
	}

	// Routing and flow files live beside the other tenant configuration unless
	// pointed elsewhere.
	if cfg.RoutingPath == "" {
		cfg.RoutingPath = cfg.TenantsPath
	}

	return cfg, nil
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
