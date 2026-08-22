package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaClient talks to Ollama's native /api/chat endpoint.
//
// The native endpoint is preferred over the OpenAI-compatible /v1 one Ollama
// also serves, because it returns thinking, content and tool_calls as separate
// fields. /v1 folds reasoning into <think> tags inside content, which is one bad
// parse away from speaking the model's scratchpad to a caller. That filtering
// still happens here (stripThinkTags) — think:false is a request, not a
// guarantee, and has been measured not holding — but the separate field means it
// is a second line of defense rather than the only one.
type OllamaClient struct {
	serverURL  string
	httpClient *http.Client
	model      string
	keepAlive  string
}

// DefaultKeepAlive holds the model resident far longer than Ollama's 5-minute
// default. The cost is memory the deployment already sized for; the benefit is
// that a caller never pays a model load inside their turn budget.
const DefaultKeepAlive = "30m"

// defaultTimeout bounds a request when the caller did not choose. Callers should
// choose: it must exceed the largest turn budget, or it silently overrides it.
const defaultTimeout = 60 * time.Second

// readinessTimeout bounds the reachability check. Short on purpose: it answers
// "is anything there", not "is it fast".
const readinessTimeout = 3 * time.Second

// NewOllamaClient creates a client for Ollama's native API.
func NewOllamaClient(cfg Config) *OllamaClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	keepAlive := cfg.KeepAlive
	if keepAlive == "" {
		keepAlive = DefaultKeepAlive
	}

	// No default is invented here: New() owns endpoint resolution, so an empty
	// URL means genuinely unconfigured and Ready() must say so rather than
	// quietly probing localhost.
	return &OllamaClient{
		serverURL:  cfg.ServerURL,
		model:      cfg.Model,
		keepAlive:  keepAlive,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *OllamaClient) Provider() Provider { return ProviderOllama }

// ServerURL reports the configured Ollama base URL, for logging and probes.
func (c *OllamaClient) ServerURL() string { return c.serverURL }

// Ready reports whether the LLM server actually answers.
//
// This used to return `serverURL != ""`, which is a statement about the flags,
// not about the world — it was true for a URL pointing at nothing at all. Since
// the supervisor is useless without a model, "ready" has to mean the server
// responded.
func (c *OllamaClient) Ready() bool {
	if c.serverURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()

	_, err := c.ListModels(ctx)
	return err == nil
}

// nativeChatRequest is the /api/chat request body.
type nativeChatRequest struct {
	Model    string          `json:"model"`
	Messages []NativeMessage `json:"messages"`
	Tools    []ToolDef       `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Think    bool            `json:"think"`
	// KeepAlive tells Ollama how long to hold the model in memory after this
	// request. Omitted, Ollama unloads after 5 idle minutes — and a PBX is idle
	// most of the night, so the first call every morning pays the full multi-GB
	// load inside the caller's turn budget. Sending it is what makes a warmed
	// model stay warm. Format is Ollama's: "30m", or "-1" for indefinitely.
	KeepAlive string `json:"keep_alive,omitempty"`
}

// nativeChatResponse is the /api/chat response body (non-streaming).
type nativeChatResponse struct {
	Message NativeMessage `json:"message"`
}

// ChatNative performs one tool-calling turn against Ollama's native /api/chat
// endpoint with thinking disabled. thinking/content/tool_calls come back as
// separate fields, so reasoning never leaks into the spoken content.
func (c *OllamaClient) ChatNative(ctx context.Context, messages []NativeMessage, tools []ToolDef, model string) (*ChatResult, error) {
	if c.serverURL == "" {
		return nil, fmt.Errorf("LLM server not configured")
	}
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, fmt.Errorf("no model configured for the Ollama provider")
	}

	reqBody := nativeChatRequest{
		Model:     model,
		Messages:  messages,
		Tools:     tools,
		Stream:    false,
		Think:     false,
		KeepAlive: c.keepAlive,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.serverURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM server error (status %d): %s", resp.StatusCode, string(body))
	}

	var nr nativeChatResponse
	if err := json.Unmarshal(body, &nr); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// think:false asks the model not to reason, but it is a request and models
	// have been measured ignoring it — qwen3:4b wrote its chain of thought into
	// content on every trial. Filter rather than trust: content is what gets
	// spoken.
	content, inline := stripThinkTags(nr.Message.Content)
	thinking := joinThinking(nr.Message.Thinking, inline)

	return &ChatResult{
		Content:   content,
		Thinking:  thinking,
		ToolCalls: nr.Message.ToolCalls,
	}, nil
}

// tagsResponse is the /api/tags body, trimmed to what the probe reads.
type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListModels returns the model names the server currently has pulled.
func (c *OllamaClient) ListModels(ctx context.Context) ([]string, error) {
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
func (c *OllamaClient) HasModel(ctx context.Context, model string) (bool, error) {
	names, err := c.ListModels(ctx)
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
func (c *OllamaClient) Warm(ctx context.Context, model string) (time.Duration, error) {
	start := time.Now()
	_, err := c.ChatNative(ctx, []NativeMessage{{Role: "user", Content: "hi"}}, nil, model)
	return time.Since(start), err
}

// ProbeProfile reports how Ollama should be probed: its model listing is
// conclusive (it cannot run what it has not pulled) and warming genuinely
// preloads.
func (c *OllamaClient) ProbeProfile() ProbeProfile {
	return ProbeProfile{
		ModelListAuthoritative: true,
		Warmable:               true,
		MissingModelHint:       "Pull it (ollama pull) or fix --llm-model",
		ProbeTimeout:           5 * time.Second,
	}
}
