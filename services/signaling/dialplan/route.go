package dialplan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Route represents a matched route with pattern and actions.
type Route struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Pattern  string         `json:"pattern"`  // Exact match, "prefix*" for prefix, or "*" for default
	Priority int            `json:"priority"` // Lower = higher priority (0 = highest)
	Enabled  bool           `json:"enabled"`
	Actions  []ActionConfig `json:"actions"`

	// Compiled pattern info (not exported, built on validation)
	isDefault bool
	isPrefix  bool
	prefix    string
	exact     string
}

// ActionConfig holds raw action configuration.
type ActionConfig struct {
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

// Validate checks the route configuration and compiles the pattern.
func (r *Route) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("route ID required")
	}
	if r.Pattern == "" {
		return fmt.Errorf("pattern required")
	}
	if len(r.Actions) == 0 {
		return fmt.Errorf("at least one action required")
	}

	// Compile pattern
	if r.Pattern == "*" {
		r.isDefault = true
	} else if strings.HasSuffix(r.Pattern, "*") {
		r.isPrefix = true
		r.prefix = strings.TrimSuffix(r.Pattern, "*")
	} else {
		r.exact = r.Pattern
	}

	return nil
}

// Match checks if a destination matches this route's pattern.
func (r *Route) Match(destination string) bool {
	if !r.Enabled {
		return false
	}

	if r.isDefault {
		return true
	}
	if r.isPrefix {
		return strings.HasPrefix(destination, r.prefix)
	}
	return destination == r.exact
}

// RouteList is a sortable list of routes by priority.
type RouteList []*Route

func (r RouteList) Len() int           { return len(r) }
func (r RouteList) Less(i, j int) bool { return r[i].Priority < r[j].Priority }
func (r RouteList) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }

// Sort sorts routes by priority (lower = higher priority).
func (r RouteList) Sort() {
	sort.Sort(r)
}

// Match finds the first matching route for a destination.
func (r RouteList) Match(destination string) (*Route, bool) {
	for _, route := range r {
		if route.Match(destination) {
			return route, true
		}
	}
	return nil, false
}
