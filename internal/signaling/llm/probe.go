package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// The startup banner echoes whatever flags it was given, so a supervisor
// pointed at a dead Ollama, or at a live one missing the configured model, looks
// like a perfectly healthy boot. The failure surfaces on the first real call —
// mid-INVITE, with a caller on the line — as a turn deadline, which reads like
// "the model is broken" rather than "the model was never there".
//
// This mirrors rtpmanager/server/probe.go, for the same reason and with the same
// posture: warn, never block. The supervisor is not required for a call to be
// routed — deterministic resolution handles a registered extension with no model
// at all — so a missing LLM must not stop the SIP stack coming up.

// probeTimeout bounds the reachability and model-listing check.
const probeTimeout = 5 * time.Second

// warmupTimeout bounds the load-the-model request. It is generous because that
// is the entire point: this call exists to absorb a multi-gigabyte model load so
// that a caller never does.
const warmupTimeout = 5 * time.Minute

// tagsResponse is the /api/tags body, trimmed to what the probe reads.
type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// listModels returns the model names the server currently has pulled.
func (c *Client) listModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serverURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s/api/tags", resp.StatusCode, c.serverURL)
	}

	var tags tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("parse /api/tags: %w", err)
	}

	names := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// HasModel reports whether the server has the named model pulled.
func (c *Client) HasModel(ctx context.Context, model string) (bool, error) {
	names, err := c.listModels(ctx)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == model {
			return true, nil
		}
	}
	return false, nil
}

// Warm sends a minimal chat request so Ollama loads the model into memory. It
// returns how long that took, which is the number worth watching: it is exactly
// what the first caller would otherwise have paid inside their turn budget.
func (c *Client) Warm(ctx context.Context, model string) (time.Duration, error) {
	start := time.Now()
	_, err := c.ChatNative(ctx, []NativeMessage{{Role: "user", Content: "hi"}}, nil, model)
	return time.Since(start), err
}

// ProbeAndWarm checks the supervisor's model is reachable and present, then
// loads it. It logs its findings and never returns an error, because nothing it
// discovers should stop the server: a deployment whose LLM is down still routes
// every call its tenant routing tables can resolve.
//
// Run it in a goroutine — a cold model load can take minutes, and the SIP stack
// must not wait on it.
func ProbeAndWarm(ctx context.Context, c *Client, model string, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "llm-probe", "server", c.ServerURL(), "model", model)

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	names, err := c.listModels(probeCtx)
	cancel()
	if err != nil {
		log.Warn("LLM server did not answer; supervised calls will fail until it does. "+
			"Deterministic routing is unaffected — check --llm-server",
			"error", err)
		return
	}

	present := false
	for _, n := range names {
		if n == model {
			present = true
			break
		}
	}
	if !present {
		// The single most confusing failure this probe exists to catch: the
		// server is healthy, so every other signal looks fine, and each call
		// fails on a model the host has never heard of.
		log.Warn("LLM server is up but does NOT have the configured model; every supervised call will fail. "+
			"Pull it (ollama pull) or fix --llm-model",
			"available", names)
		return
	}

	warmCtx, cancelWarm := context.WithTimeout(ctx, warmupTimeout)
	defer cancelWarm()

	took, err := c.Warm(warmCtx, model)
	if err != nil {
		log.Warn("LLM model present but the warm-up request failed; the first caller will pay the model load",
			"error", err, "elapsed", took.Round(time.Millisecond))
		return
	}

	log.Info("LLM ready and warmed", "load_time", took.Round(time.Millisecond),
		"note", "this is what the first caller would otherwise have waited inside their turn budget")
}
