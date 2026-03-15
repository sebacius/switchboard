package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// Client is a Whisper ASR server client (OpenAI-compatible API)
type Client struct {
	serverURL  string
	httpClient *http.Client
}

// Config holds ASR client configuration
type Config struct {
	ServerURL string        // Base URL of Whisper server (e.g., "http://localhost:8001")
	Timeout   time.Duration // Request timeout (default 30s)
}

// NewClient creates a new ASR client
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

// TranscriptionResponse is the response from the Whisper API
type TranscriptionResponse struct {
	Text string `json:"text"`
}

// Transcribe sends audio data to Whisper and returns transcribed text
// audioData should be WAV format (16-bit PCM)
func (c *Client) Transcribe(ctx context.Context, audioData []byte) (string, error) {
	if c.serverURL == "" {
		return "", fmt.Errorf("ASR server not configured")
	}

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add audio file
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("write audio data: %w", err)
	}

	// Close multipart writer
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	// Create request
	url := c.serverURL + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ASR request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ASR server error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse JSON response
	var result TranscriptionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	return result.Text, nil
}

// Ready checks if the ASR server is configured
func (c *Client) Ready() bool {
	return c.serverURL != ""
}
