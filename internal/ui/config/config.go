package config

import (
	"flag"
	"os"
	"strings"
)

// Backend represents a signaling server instance
type Backend struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"` // HTTP address e.g., http://localhost:8080
}

// Config holds the UI server configuration
type Config struct {
	// HTTP server settings
	Port     int
	BindAddr string

	// Backend signaling servers
	Backends []Backend

	// Log level
	LogLevel string
}

// Load loads configuration from command line flags and environment variables
func Load() *Config {
	cfg := &Config{}

	// Define flags
	flag.IntVar(&cfg.Port, "port", 3000, "UI HTTP server port")
	flag.StringVar(&cfg.BindAddr, "bind", "0.0.0.0", "UI bind address")
	flag.StringVar(&cfg.LogLevel, "loglevel", "info", "Log level (debug, info, warn, error)")

	var backends string
	flag.StringVar(&backends, "backends", "http://localhost:8080", "Comma-separated list of signaling server addresses (name=addr or just addr)")

	flag.Parse()

	// Parse backend addresses
	cfg.Backends = parseBackends(backends)

	// Override with environment variables if set
	if port := os.Getenv("UI_PORT"); port != "" {
		if p := stringToInt(port); p > 0 {
			cfg.Port = p
		}
	}
	if bind := os.Getenv("UI_BIND"); bind != "" {
		cfg.BindAddr = bind
	}
	if loglevel := os.Getenv("UI_LOGLEVEL"); loglevel != "" {
		cfg.LogLevel = loglevel
	}
	if envBackends := os.Getenv("UI_BACKENDS"); envBackends != "" {
		cfg.Backends = parseBackends(envBackends)
	}

	return cfg
}

// parseBackends parses a comma-separated list of backend addresses
// Format: "name=http://host:port" or "http://host:port" (name auto-generated)
func parseBackends(s string) []Backend {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	backends := make([]Backend, 0, len(parts))

	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		var backend Backend
		if idx := strings.Index(p, "="); idx > 0 {
			// Format: name=address
			backend.Name = strings.TrimSpace(p[:idx])
			backend.Address = strings.TrimSpace(p[idx+1:])
		} else {
			// Auto-generate name
			backend.Address = p
			if len(parts) == 1 {
				backend.Name = "default"
			} else {
				backend.Name = strings.ReplaceAll(p, "http://", "")
				backend.Name = strings.ReplaceAll(backend.Name, "https://", "")
				if backend.Name == "" {
					backend.Name = string(rune('A' + i))
				}
			}
		}

		// Ensure address has scheme
		if !strings.HasPrefix(backend.Address, "http://") && !strings.HasPrefix(backend.Address, "https://") {
			backend.Address = "http://" + backend.Address
		}

		backends = append(backends, backend)
	}

	return backends
}

func stringToInt(s string) int {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
