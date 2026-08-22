package agent

import (
	"path/filepath"
	"testing"
)

// The shipped policy is configuration, and configuration that does not load is
// a deployment with no authorization boundary. This loads the real file rather
// than a fixture.
func TestShippedPolicyConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "..", "resources", "config", "policy.json")

	cfg, err := LoadPolicyConfig(path)
	if err != nil {
		t.Fatalf("the shipped policy config must load: %v", err)
	}
	if cfg.DefaultChannelLimit <= 0 {
		t.Fatalf("expected a positive default channel limit, got %d", cfg.DefaultChannelLimit)
	}
}
