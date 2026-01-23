package dialplan

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
)

// Config represents the JSON configuration structure.
type Config struct {
	Version string  `json:"version"`
	Routes  []Route `json:"routes"`
}

// Dialplan provides thread-safe access to routing configuration.
// Uses copy-on-write semantics for lock-free reads.
type Dialplan struct {
	routes atomic.Pointer[RouteList]
	path   string
	logger *slog.Logger
}

// New creates a new Dialplan from a JSON config file.
func New(path string, logger *slog.Logger) (*Dialplan, error) {
	if logger == nil {
		logger = slog.Default()
	}

	d := &Dialplan{
		path:   path,
		logger: logger,
	}

	if err := d.Reload(); err != nil {
		return nil, fmt.Errorf("initial load: %w", err)
	}

	return d, nil
}

// Match finds the first matching route for the destination.
// Thread-safe: uses atomic load for lock-free reads.
func (d *Dialplan) Match(destination string) (*Route, bool) {
	routes := d.routes.Load()
	if routes == nil {
		return nil, false
	}
	return routes.Match(destination)
}

// Reload reloads configuration from the file.
// Thread-safe: atomic swap after successful parse.
func (d *Dialplan) Reload() error {
	data, err := os.ReadFile(d.path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Validate and compile all routes
	routes := make(RouteList, 0, len(cfg.Routes))
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		if err := route.Validate(); err != nil {
			return fmt.Errorf("route %d (%s): %w", i, route.ID, err)
		}
		routes = append(routes, route)
	}

	// Sort by priority
	routes.Sort()

	// Atomic swap
	d.routes.Store(&routes)

	d.logger.Info("[Dialplan] Loaded routes",
		"path", d.path,
		"count", len(routes),
		"version", cfg.Version,
	)

	return nil
}

// RouteCount returns the number of loaded routes.
func (d *Dialplan) RouteCount() int {
	routes := d.routes.Load()
	if routes == nil {
		return 0
	}
	return len(*routes)
}
