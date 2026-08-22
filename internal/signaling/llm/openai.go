package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIClient talks to the OpenAI /v1/chat/completions API, and to anything
// that implements it — vLLM, Groq, OpenRouter, LiteLLM, a local gateway. The
// only difference between those and OpenAI itself is the base URL and whether a
// key is needed, so they are one code path.
//
// Adopting /v1 means adopting the endpoint this repo originally rejected: it has
// no separate reasoning field, so a model's scratchpad arrives either in a
// provider-specific field or folded into content as <think> tags. Both are
// routed to Thinking here, and only Content is ever eligible for TTS.
type OpenAIClient struct {
	serverURL  string // no trailing slash, no /v1 suffix
	apiKey     string
	httpClient *http.Client
	model      string
}

// ErrUnauthorized marks a 401/403 so the probe can say "check the key" instead
// of "the server did not answer" — the two failures look identical in a log and
// have completely different fixes.
var ErrUnauthorized = errors.New("LLM server rejected our credentials")

// newOpenAIClient validates the credential rule: a key is required for the
// hosted API and optional for anything else, because a self-hosted gateway with
// no auth is a normal deployment and demanding a placeholder key would only
// teach operators to invent one.
func newOpenAIClient(cfg Config) (*OpenAIClient, error) {
	// New() owns endpoint resolution; an empty URL here means unconfigured, and
	// stays that way so Ready() reports the truth.
	serverURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")

	if cfg.APIKey == "" && isHostedOpenAI(serverURL) {
		return nil, fmt.Errorf(
			"provider %q against %s needs an API key: set OPENAI_API_KEY in the environment "+
				"(it is deliberately not a flag, because flags are visible in ps and in the startup banner)",
			ProviderOpenAI, serverURL)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	return &OpenAIClient{
		serverURL:  serverURL,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// isHostedOpenAI reports whether the URL is OpenAI's own API, which is the only
// endpoint we know for certain requires a key.
func isHostedOpenAI(serverURL string) bool {
	u := strings.TrimRight(strings.ToLower(strings.TrimSpace(serverURL)), "/")
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return u == "api.openai.com" || strings.HasPrefix(u, "api.openai.com/")
}

func (c *OpenAIClient) Provider() Provider { return ProviderOpenAI }
func (c *OpenAIClient) ServerURL() string  { return c.serverURL }

// --- wire types -------------------------------------------------------------
//
// These are deliberately separate from NativeMessage rather than extra json tags
// on it. /v1 rejects message properties it does not recognize, so `thinking` and
// `tool_name` must be ABSENT, not empty — which no combination of omitempty can
// express on a shared struct.

type oaiChatRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []ToolDef    `json:"tools,omitempty"`
	Stream   bool         `json:"stream"`
	// No think, no keep_alive: neither exists in /v1, and unrecognized top-level
	// arguments are rejected.
}

type oaiMessage struct {
	Role string `json:"role"`
	// Content is NOT omitempty: an assistant message with neither content nor
	// tool_calls is a 400, and the runner records whatever the model returned.
	Content    string        `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"` // always "function"
	Function oaiToolCallFunc `json:"function"`
}

type oaiToolCallFunc struct {
	Name string `json:"name"`
	// Arguments is a JSON *string* here, where Ollama sends a decoded object.
	Arguments string `json:"arguments"`
}

type oaiChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string        `json:"role"`
			Content   string        `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
			// Neither of these is in the OpenAI spec; both are what gateways
			// serving reasoning models actually emit (vLLM and DeepSeek use
			// reasoning_content, OpenRouter uses reasoning). Reading them is
			// what keeps a caller from hearing a scratchpad.
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type oaiModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// --- translation ------------------------------------------------------------

// toOpenAIMessages translates the provider-agnostic conversation into the /v1
// wire format. It is a real translation, not a re-tagging: Thinking and ToolName
// are dropped because /v1 rejects them, which means the model does not see its
// own prior reasoning on this provider. That is a real difference from Ollama and
// a harmless one — the runner keeps Thinking only so the model can see its own
// scratchpad.
func toOpenAIMessages(msgs []NativeMessage) []oaiMessage {
	out := make([]oaiMessage, 0, len(msgs))

	// pending maps a tool name to the ids advertised by the most recent assistant
	// message and not yet answered. It exists only for the synthesized-id path: a
	// result whose own id is missing has to be matched back to the call it
	// answers, and the tool name is the only other thing that identifies it. Two
	// independently synthesized ids would not match, which is a 400 — exactly the
	// malformed request the fallback is supposed to prevent.
	pending := map[string][]string{}

	for i, m := range msgs {
		om := oaiMessage{Role: m.Role, Content: m.Content}

		if m.Role == "tool" {
			// A tool result is correlated by id alone. `name` is deprecated on
			// tool messages and some gateways reject it.
			om.ToolCallID = m.ToolCallID
			if om.ToolCallID == "" {
				if ids := pending[m.ToolName]; len(ids) > 0 {
					om.ToolCallID = ids[0]
					pending[m.ToolName] = ids[1:]
				} else {
					om.ToolCallID = syntheticCallID(i, 0)
				}
			}
			out = append(out, om)
			continue
		}

		if len(m.ToolCalls) > 0 {
			// A new assistant turn: anything still unanswered from the previous
			// one never will be.
			pending = map[string][]string{}
		}

		for j, tc := range m.ToolCalls {
			id := tc.ID
			if id == "" {
				// Never fires against a real response, where the id round-trips
				// (and where ChatNative already fills one in). It makes the
				// translation total rather than conditionally malformed, covering
				// a scripted history or a gateway that omits ids entirely.
				id = syntheticCallID(i, j)
				pending[tc.Function.Name] = append(pending[tc.Function.Name], id)
			}
			args, err := json.Marshal(tc.Function.Arguments)
			if err != nil {
				args = []byte("{}")
			}
			om.ToolCalls = append(om.ToolCalls, oaiToolCall{
				ID:       id,
				Type:     "function",
				Function: oaiToolCallFunc{Name: tc.Function.Name, Arguments: string(args)},
			})
		}
		out = append(out, om)
	}
	return out
}

func syntheticCallID(msgIdx, callIdx int) string {
	return fmt.Sprintf("call_%d_%d", msgIdx, callIdx)
}

// decodeToolArguments turns OpenAI's JSON-string arguments into the parsed map
// the runner and every tool handler expect.
//
// A value it cannot parse yields empty arguments rather than an error, on
// purpose. Every consequential tool already validates its arguments and returns
// an actionable message the model corrects on the next turn; routing a
// formatting slip into that path costs one turn. Failing the turn instead would
// make the caller hear the "assistant is unavailable" apology over something
// entirely recoverable.
func decodeToolArguments(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{}, false
	}
	if args == nil {
		// "null" already handled, but a bare JSON null elsewhere would leave a
		// nil map, and tool handlers read from it directly.
		return map[string]any{}, true
	}
	return args, true
}

// --- requests ---------------------------------------------------------------

func (c *OpenAIClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.serverURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		// Only when we have one: an empty Bearer is worse than no header at all,
		// since a gateway that allows anonymous access would reject it.
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

// ChatNative performs one tool-calling turn against /v1/chat/completions.
func (c *OpenAIClient) ChatNative(ctx context.Context, messages []NativeMessage, tools []ToolDef, model string) (*ChatResult, error) {
	if c.serverURL == "" {
		return nil, fmt.Errorf("LLM server not configured")
	}
	if model == "" {
		model = c.model
	}
	if model == "" {
		return nil, fmt.Errorf("no model configured for the OpenAI provider")
	}

	reqBody := oaiChatRequest{
		Model:    model,
		Messages: toOpenAIMessages(messages),
		Tools:    tools,
		Stream:   false,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var cr oaiChatResponse
	// Decode before the status check so a provider's own error message can be
	// preferred over a wall of JSON.
	parseErr := json.Unmarshal(body, &cr)

	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(body))
		if parseErr == nil && cr.Error != nil && cr.Error.Message != "" {
			detail = cr.Error.Message
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w (status %d): %s", ErrUnauthorized, resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("LLM server error (status %d): %s", resp.StatusCode, detail)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parse response: %w", parseErr)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	m := cr.Choices[0].Message

	// Reasoning may arrive in a field of its own, folded into content as <think>
	// tags, or both. All of it goes to Thinking; only what survives in Content is
	// ever spoken.
	content, inline := stripThinkTags(m.Content)
	thinking := joinThinking(m.ReasoningContent, m.Reasoning, inline)

	calls := make([]ToolCall, 0, len(m.ToolCalls))
	for i, tc := range m.ToolCalls {
		args, _ := decodeToolArguments(tc.Function.Arguments)
		id := tc.ID
		if id == "" {
			id = syntheticCallID(0, i)
		}
		calls = append(calls, ToolCall{
			ID:       id,
			Function: ToolCallFunction{Name: tc.Function.Name, Arguments: args},
		})
	}

	return &ChatResult{Content: content, Thinking: thinking, ToolCalls: calls}, nil
}

// ListModels returns the model ids the server advertises. Unlike Ollama's, this
// listing is advisory: gateways serve models they do not enumerate.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w (status %d) from %s/v1/models", ErrUnauthorized, resp.StatusCode, c.serverURL)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s/v1/models", resp.StatusCode, c.serverURL)
	}

	var mr oaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("parse /v1/models: %w", err)
	}

	names := make([]string, 0, len(mr.Data))
	for _, m := range mr.Data {
		names = append(names, m.ID)
	}
	return names, nil
}

// Ready reports whether the server actually answers, rather than whether a URL
// was configured.
func (c *OpenAIClient) Ready() bool {
	if c.serverURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()

	_, err := c.ListModels(ctx)
	return err == nil
}

// Warm is a no-op. A hosted provider serves models it already holds, so there is
// no load to absorb — a warm-up request would buy nothing and may be billed.
func (c *OpenAIClient) Warm(context.Context, string) (time.Duration, error) { return 0, nil }

// ProbeProfile reports how this provider should be probed: its model listing is
// advisory, and there is nothing to warm.
func (c *OpenAIClient) ProbeProfile() ProbeProfile {
	return ProbeProfile{
		ModelListAuthoritative: false,
		Warmable:               false,
		MissingModelHint:       "Check the model id against the provider's catalog, or fix --llm-model",
		// A WAN TLS handshake, not a loopback call.
		ProbeTimeout: 10 * time.Second,
	}
}
