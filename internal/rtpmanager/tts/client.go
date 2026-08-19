package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a TTS server client (OpenAI-compatible API)
type Client struct {
	serverURL  string
	httpClient *http.Client
}

// Config holds TTS client configuration
type Config struct {
	ServerURL string        // Base URL of TTS server (e.g., "http://localhost:8000")
	Timeout   time.Duration // Request timeout (default 30s)
}

// NewClient creates a new TTS client
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		serverURL: cfg.ServerURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// DefaultVoice is used when a caller does not name one. Piper rejects an empty
// voice with `Error loading voice: , KeyError: ”` (HTTP 400), so "unset" is
// never a usable value to forward — there is nothing sensible for the server to
// fall back to on our behalf.
const DefaultVoice = "alloy"

// SpeechRequest is the request body for the TTS API
type SpeechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format,omitempty"`
}

// Synthesize generates speech audio from text
// Returns WAV audio data (22kHz, 16-bit, mono)
func (c *Client) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	if c.serverURL == "" {
		return nil, fmt.Errorf("TTS server not configured")
	}

	if voice == "" {
		voice = DefaultVoice
	}

	reqBody := SpeechRequest{
		Model:          "tts-1",
		Input:          text,
		Voice:          voice,
		ResponseFormat: "wav",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.serverURL + "/v1/audio/speech"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TTS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TTS server error (status %d): %s", resp.StatusCode, string(body))
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return audioData, nil
}

// Ready checks if the TTS server is configured
func (c *Client) Ready() bool {
	return c.serverURL != ""
}
