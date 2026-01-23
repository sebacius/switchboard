package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	types "github.com/sebas/switchboard/api/types/v1"
)

// Client is an HTTP client for a signaling server API
type Client struct {
	name       string
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new signaling API client
func NewClient(name, baseURL string) *Client {
	return &Client{
		name:    name,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the backend name
func (c *Client) Name() string {
	return c.name
}

// BaseURL returns the backend base URL
func (c *Client) BaseURL() string {
	return c.baseURL
}

// Health fetches health status from the signaling server
func (c *Client) Health(ctx context.Context) (*types.HealthResponse, error) {
	resp, err := c.get(ctx, "/api/v1/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var health types.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decode health: %w", err)
	}
	return &health, nil
}

// Stats fetches statistics from the signaling server
func (c *Client) Stats(ctx context.Context) (*types.StatsResponse, error) {
	resp, err := c.get(ctx, "/api/v1/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats types.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	return &stats, nil
}

// Registrations fetches all registrations from the signaling server
func (c *Client) Registrations(ctx context.Context) ([]types.Registration, error) {
	resp, err := c.get(ctx, "/api/v1/registrations")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var regs []types.Registration
	if err := json.NewDecoder(resp.Body).Decode(&regs); err != nil {
		return nil, fmt.Errorf("decode registrations: %w", err)
	}
	return regs, nil
}

// Dialogs fetches all dialogs from the signaling server
func (c *Client) Dialogs(ctx context.Context) ([]types.Dialog, error) {
	resp, err := c.get(ctx, "/api/v1/dialogs")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var dialogs []types.Dialog
	if err := json.NewDecoder(resp.Body).Decode(&dialogs); err != nil {
		return nil, fmt.Errorf("decode dialogs: %w", err)
	}
	return dialogs, nil
}

// Sessions fetches all RTP sessions from the signaling server
func (c *Client) Sessions(ctx context.Context) ([]types.Session, error) {
	resp, err := c.get(ctx, "/api/v1/sessions")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var sessions []types.Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return sessions, nil
}

// RtpManagers fetches RTP manager pool status from the signaling server
func (c *Client) RtpManagers(ctx context.Context) (*types.RtpManagersResponse, error) {
	resp, err := c.get(ctx, "/api/v1/rtpmanagers")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var managers types.RtpManagersResponse
	if err := json.NewDecoder(resp.Body).Decode(&managers); err != nil {
		return nil, fmt.Errorf("decode rtpmanagers: %w", err)
	}
	return &managers, nil
}

// DrainStatus represents the status of a drain operation
type DrainStatus struct {
	NodeID           string `json:"node_id"`
	State            string `json:"state"`
	Mode             string `json:"mode"`
	InitialSessions  int    `json:"initial_sessions"`
	RemainingSessions int   `json:"remaining_sessions"`
	StartedAt        string `json:"started_at,omitempty"`
}

// StartDrain initiates a drain operation on an RTP manager node
func (c *Client) StartDrain(ctx context.Context, nodeID, mode string) (*DrainStatus, error) {
	path := fmt.Sprintf("/api/v1/rtpmanagers/%s/drain?mode=%s", nodeID, mode)
	resp, err := c.post(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var status DrainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode drain status: %w", err)
	}
	return &status, nil
}

// GetDrainStatus fetches the current drain status for an RTP manager node
func (c *Client) GetDrainStatus(ctx context.Context, nodeID string) (*DrainStatus, error) {
	path := fmt.Sprintf("/api/v1/rtpmanagers/%s/drain", nodeID)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var status DrainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode drain status: %w", err)
	}
	return &status, nil
}

// CancelDrain cancels an in-progress drain operation
func (c *Client) CancelDrain(ctx context.Context, nodeID string) error {
	path := fmt.Sprintf("/api/v1/rtpmanagers/%s/drain", nodeID)
	resp, err := c.delete(ctx, path)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// get performs an HTTP GET request
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return resp, nil
}

// post performs an HTTP POST request
func (c *Client) post(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return resp, nil
}

// delete performs an HTTP DELETE request
func (c *Client) delete(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return resp, nil
}
