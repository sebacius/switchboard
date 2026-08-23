package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	NodeID            string `json:"node_id"`
	State             string `json:"state"`
	Mode              string `json:"mode"`
	InitialSessions   int    `json:"initial_sessions"`
	RemainingSessions int    `json:"remaining_sessions"`
	StartedAt         string `json:"started_at,omitempty"`
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

// ParkedCalls fetches all parked calls from the signaling server
func (c *Client) ParkedCalls(ctx context.Context) ([]types.ParkedCall, error) {
	resp, err := c.get(ctx, "/api/v1/park")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var calls []types.ParkedCall
	if err := json.NewDecoder(resp.Body).Decode(&calls); err != nil {
		return nil, fmt.Errorf("decode parked calls: %w", err)
	}
	return calls, nil
}

// ForceUnpark force-unparks a call from the specified slot
func (c *Client) ForceUnpark(ctx context.Context, slotID string) error {
	path := fmt.Sprintf("/api/v1/park/%s", slotID)
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
		return nil, errorFrom(resp)
	}

	return resp, nil
}

// HTTPError is a non-2xx response, carrying the body so a caller can read the
// reason rather than guessing from the status.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("unexpected status %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("unexpected status: %d", e.StatusCode)
}

// errorFrom turns a non-2xx response into an HTTPError, closing the body.
//
// Every verb goes through it, because the body carries WHY: a refused
// configuration write puts the individual problems there, and discarding it
// reduces "node greeting has an unwired timeout exit" to "422".
func errorFrom(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	return &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
}

// rejectionFrom upgrades a 422 into a typed ConfigRejectedError so every editor
// can show which node is wrong, not only the one write that happened to unpack
// it. Anything else passes through unchanged.
func rejectionFrom(err error) error {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnprocessableEntity {
		var rejection types.ConfigRejection
		if json.Unmarshal([]byte(httpErr.Body), &rejection) == nil && len(rejection.Problems) > 0 {
			return &ConfigRejectedError{Rejection: rejection}
		}
	}
	return err
}

// tenantPath builds a tenant file URL. The name is escaped because the server's
// name rule admits characters — an ampersand, notably — that would otherwise
// terminate the path or the query.
func tenantPath(name, file string) string {
	path := "/api/v1/config/tenants/" + url.PathEscape(name)
	if file != "" {
		path += "?file=" + url.QueryEscape(file)
	}
	return path
}

// decodeSaved reads the body of a successful write, which carries any warnings
// the validator raised.
func decodeSaved(resp *http.Response) ([]types.ConfigProblem, error) {
	defer resp.Body.Close()
	var saved types.ConfigSaved
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		// The write succeeded; only the warning list is lost.
		return nil, nil
	}
	return saved.Warnings, nil
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
		return nil, errorFrom(resp)
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
		return nil, errorFrom(resp)
	}

	return resp, nil
}

// put performs an HTTP PUT request with a JSON body
func (c *Client) put(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errorFrom(resp)
	}

	return resp, nil
}

// postWithBody performs an HTTP POST request with a JSON body
func (c *Client) postWithBody(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return nil, errorFrom(resp)
	}

	return resp, nil
}

// --- Configuration Management ---

func (c *Client) ListTenants(ctx context.Context) ([]types.TenantFile, error) {
	resp, err := c.get(ctx, "/api/v1/config/tenants")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tenants []types.TenantFile
	if err := json.NewDecoder(resp.Body).Decode(&tenants); err != nil {
		return nil, fmt.Errorf("decode tenants: %w", err)
	}
	return tenants, nil
}

// GetTenantFile fetches one of a tenant's configuration files.
func (c *Client) GetTenantFile(ctx context.Context, name, file string) (string, error) {
	return c.getTenantFile(ctx, name, file)
}

// GetTenant fetches a tenant's routing table.
func (c *Client) GetTenant(ctx context.Context, name string) (string, error) {
	return c.getTenantFile(ctx, name, "routing")
}

func (c *Client) getTenantFile(ctx context.Context, name, file string) (string, error) {
	resp, err := c.get(ctx, tenantPath(name, file))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode tenant: %w", err)
	}
	return result.Content, nil
}

// CreateTenant creates a tenant's routing file, returning any warnings the
// server raised about the content it accepted.
func (c *Client) CreateTenant(ctx context.Context, name, content string) ([]types.ConfigProblem, error) {
	body, _ := json.Marshal(types.CreateTenantRequest{Name: name, Content: content})
	resp, err := c.postWithBody(ctx, "/api/v1/config/tenants", bytes.NewReader(body))
	if err != nil {
		return nil, rejectionFrom(err)
	}
	return decodeSaved(resp)
}

// PutTenant saves a tenant's routing table.
func (c *Client) PutTenant(ctx context.Context, name, content string) ([]types.ConfigProblem, error) {
	return c.PutTenantFile(ctx, name, "routing", content)
}

// PutTenantFile saves one of a tenant's configuration files.
//
// A write that would not load is refused by the server, and the refusal carries
// the problems. Returning them as a typed error is what lets the editor show
// which node is wrong instead of a bare "bad request".
func (c *Client) PutTenantFile(ctx context.Context, name, file, content string) ([]types.ConfigProblem, error) {
	body, _ := json.Marshal(types.FileContent{Content: content})
	resp, err := c.put(ctx, tenantPath(name, file), bytes.NewReader(body))
	if err != nil {
		return nil, rejectionFrom(err)
	}
	return decodeSaved(resp)
}

// ConfigRejectedError reports a configuration write refused by validation.
type ConfigRejectedError struct {
	Rejection types.ConfigRejection
}

// Error summarizes the problems.
func (e *ConfigRejectedError) Error() string {
	if len(e.Rejection.Problems) == 1 {
		p := e.Rejection.Problems[0]
		return p.Path + ": " + p.Message
	}
	return fmt.Sprintf("%d problems, not saved", len(e.Rejection.Problems))
}

// Problems returns the individual findings.
func (e *ConfigRejectedError) Problems() []types.ConfigProblem { return e.Rejection.Problems }

// DeleteTenant removes every file belonging to a tenant.
func (c *Client) DeleteTenant(ctx context.Context, name string) error {
	resp, err := c.delete(ctx, tenantPath(name, ""))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// --- Deployment-wide configuration ---

// ListGlobalFiles describes policy.json, routes.json and trunk_peers.json.
func (c *Client) ListGlobalFiles(ctx context.Context) ([]types.GlobalFile, error) {
	resp, err := c.get(ctx, "/api/v1/config/files")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var files []types.GlobalFile
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("decode config files: %w", err)
	}
	return files, nil
}

// GetGlobalFile reads one deployment-wide file, content included.
func (c *Client) GetGlobalFile(ctx context.Context, name string) (types.GlobalFile, error) {
	resp, err := c.get(ctx, "/api/v1/config/files/"+url.PathEscape(name))
	if err != nil {
		return types.GlobalFile{}, err
	}
	defer resp.Body.Close()

	var file types.GlobalFile
	if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
		return types.GlobalFile{}, fmt.Errorf("decode config file: %w", err)
	}
	return file, nil
}

// PutGlobalFile saves one deployment-wide file, returning any warnings the
// server raised about content it accepted.
func (c *Client) PutGlobalFile(ctx context.Context, name, content string) ([]types.ConfigProblem, error) {
	body, _ := json.Marshal(types.FileContent{Content: content})
	resp, err := c.put(ctx, "/api/v1/config/files/"+url.PathEscape(name), bytes.NewReader(body))
	if err != nil {
		return nil, rejectionFrom(err)
	}
	return decodeSaved(resp)
}

// ConfigStatus reports whether what is on disk is what is serving calls.
func (c *Client) ConfigStatus(ctx context.Context) (types.ConfigStatus, error) {
	resp, err := c.get(ctx, "/api/v1/config/status")
	if err != nil {
		return types.ConfigStatus{}, err
	}
	defer resp.Body.Close()

	var status types.ConfigStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return types.ConfigStatus{}, fmt.Errorf("decode config status: %w", err)
	}
	return status, nil
}

// ReloadConfig triggers a configuration reload and reports what it covered.
func (c *Client) ReloadConfig(ctx context.Context) (types.ReloadResult, error) {
	resp, err := c.post(ctx, "/api/v1/config/reload")
	if err != nil {
		return types.ReloadResult{}, err
	}
	defer resp.Body.Close()

	var result types.ReloadResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// The reload happened; only the detail is lost.
		return types.ReloadResult{Status: "ok"}, nil
	}
	return result, nil
}
