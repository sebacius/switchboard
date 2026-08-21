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

// Client is an LLM client for Ollama (OpenAI-compatible API)
type Client struct {
	serverURL  string
	httpClient *http.Client
	model      string
	keepAlive  string
}

// Config holds LLM client configuration
type Config struct {
	ServerURL string        // Base URL of Ollama server (e.g., "http://localhost:11434")
	Model     string        // Model name (e.g., "llama3")
	Timeout   time.Duration // Request timeout (default 60s)
	// KeepAlive is how long Ollama should hold the model resident after a
	// request, in its own format ("30m", "-1" for indefinitely). Empty uses
	// DefaultKeepAlive; Ollama's own default of 5 minutes is too short for a
	// phone system, where the gap between calls is routinely longer than the
	// gap the model survives.
	KeepAlive string
}

// DefaultKeepAlive holds the model resident far longer than Ollama's 5-minute
// default. The cost is memory the deployment already sized for; the benefit is
// that a caller never pays a model load inside their turn budget.
const DefaultKeepAlive = "30m"

// readinessTimeout bounds the reachability check. Short on purpose: it answers
// "is anything there", not "is it fast".
const readinessTimeout = 3 * time.Second

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`    // "system", "user", or "assistant"
	Content string `json:"content"` // Message content
}

// ChatRequest is the request body for chat completions
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// ChatResponse is the response from chat completions
type ChatResponse struct {
	Choices []Choice `json:"choices"`
}

// Choice represents a completion choice
type Choice struct {
	Message Message `json:"message"`
}

// NewClient creates a new LLM client
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	model := cfg.Model
	if model == "" {
		model = "llama3"
	}

	keepAlive := cfg.KeepAlive
	if keepAlive == "" {
		keepAlive = DefaultKeepAlive
	}

	return &Client{
		serverURL: cfg.ServerURL,
		model:     model,
		keepAlive: keepAlive,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// ServerURL reports the configured Ollama base URL, for logging and probes.
func (c *Client) ServerURL() string { return c.serverURL }

// Chat sends a conversation to the LLM and returns the assistant's response
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	return c.ChatWithModel(ctx, messages, c.model)
}

// ChatWithModel sends a conversation to the LLM using the specified model
func (c *Client) ChatWithModel(ctx context.Context, messages []Message, model string) (string, error) {
	if c.serverURL == "" {
		return "", fmt.Errorf("LLM server not configured")
	}

	reqBody := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Use OpenAI-compatible endpoint
	url := c.serverURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM server error (status %d): %s", resp.StatusCode, string(body))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return chatResp.Choices[0].Message.Content, nil
}

// Ready reports whether the LLM server actually answers.
//
// This used to return `serverURL != ""`, which is a statement about the flags,
// not about the world — it was true for a URL pointing at nothing at all. Since
// the supervisor is useless without a model, "ready" has to mean the server
// responded.
func (c *Client) Ready() bool {
	if c.serverURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()

	_, err := c.listModels(ctx)
	return err == nil
}

// Conversation manages a multi-turn conversation with context
type Conversation struct {
	client       *Client
	messages     []Message
	systemPrompt string
	model        string // per-conversation model override
}

// NewConversation creates a new conversation with an optional system prompt and model override
func (c *Client) NewConversation(systemPrompt string, model string) *Conversation {
	conv := &Conversation{
		client:       c,
		messages:     make([]Message, 0),
		systemPrompt: systemPrompt,
		model:        model,
	}

	if systemPrompt != "" {
		conv.messages = append(conv.messages, Message{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	return conv
}

// Say sends a user message and returns the assistant's response
func (conv *Conversation) Say(ctx context.Context, userMessage string) (string, error) {
	// Add user message
	conv.messages = append(conv.messages, Message{
		Role:    "user",
		Content: userMessage,
	})

	// Get response
	model := conv.model
	if model == "" {
		model = conv.client.model
	}
	response, err := conv.client.ChatWithModel(ctx, conv.messages, model)
	if err != nil {
		// Remove the failed user message
		conv.messages = conv.messages[:len(conv.messages)-1]
		return "", err
	}

	// Add assistant response to history
	conv.messages = append(conv.messages, Message{
		Role:    "assistant",
		Content: response,
	})

	return response, nil
}

// TurnCount returns the number of conversation turns (user messages)
func (conv *Conversation) TurnCount() int {
	count := 0
	for _, msg := range conv.messages {
		if msg.Role == "user" {
			count++
		}
	}
	return count
}
