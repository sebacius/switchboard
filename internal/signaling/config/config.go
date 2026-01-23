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
	BindAddr      string // Address to bind for listening
	AdvertiseAddr string // Address to advertise in SIP headers
	LogLevel      string

	// Dialplan settings
	DialplanPath string // Path to dialplan.json config file

	// RTP Manager pool settings
	// RTPManagerNodes maps node ID to address (e.g., "rtpmanager-0" -> "localhost:9090")
	// Takes precedence over RTPManagerAddrs if non-empty
	RTPManagerNodes map[string]string
	// RTPManagerAddrs is legacy format - list of addresses (auto-generates node IDs)
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
	flag.StringVar(&cfg.BindAddr, "bind", "0.0.0.0", "SIP bind address")
	flag.StringVar(&cfg.AdvertiseAddr, "advertise", "", "Address to advertise in SIP headers (auto-detected if not set)")
	flag.StringVar(&cfg.LogLevel, "loglevel", "debug", "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.DialplanPath, "dialplan", "resources/config/dialplan.json", "Path to dialplan configuration file")

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
	if dialplanPath := os.Getenv("DIALPLAN_PATH"); dialplanPath != "" {
		cfg.DialplanPath = dialplanPath
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
