// Package api provides an HTTP client for the Keld Atlas API.
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/errs"
)

// DeviceStart holds the response from the device-start endpoint.
type DeviceStart struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// Onboarding holds the response from the onboarding endpoint.
type Onboarding struct {
	Endpoint    string `json:"endpoint"`
	IngestToken string `json:"ingest_token"`
	Actor       string `json:"actor"`
}

// Client is an HTTP client for the Atlas API.
type Client struct {
	BaseURL string
	token   string
	http    *http.Client
}

// NewClient returns a new Client. token may be "" to indicate no authentication.
// Any trailing slash on baseURL is stripped.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// DeviceStart calls POST /v1/cli/device/start and returns the parsed response.
func (c *Client) DeviceStart() (*DeviceStart, error) {
	resp, err := c.post("/v1/cli/device/start", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var ds DeviceStart
	if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
		return nil, errs.New("Atlas returned invalid JSON: %v", err)
	}
	return &ds, nil
}

// DevicePoll calls POST /v1/cli/device/poll. HTTP 202 (pending) returns (nil, nil).
func (c *Client) DevicePoll(deviceCode string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	resp, err := c.post("/v1/cli/device/poll", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil, nil
	}
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, errs.New("Atlas returned invalid JSON: %v", err)
	}
	return result, nil
}

// Enroll calls POST /v1/cli/enroll to redeem a one-time setup code, returning
// the same {access_token, principal, org} payload as a successful device poll.
func (c *Client) Enroll(code string) (map[string]any, error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	resp, err := c.post("/v1/cli/enroll", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusGone {
		return nil, errs.New("invalid or expired setup code")
	}
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, errs.New("Atlas returned invalid JSON: %v", err)
	}
	return result, nil
}

// Onboarding calls GET /v1/cli/onboarding with a Bearer token.
// Returns an error if no token was provided at construction time.
func (c *Client) Onboarding() (*Onboarding, error) {
	if c.token == "" {
		return nil, errs.New("onboarding requires authentication")
	}
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/v1/cli/onboarding", nil)
	if err != nil {
		return nil, errs.New("network error contacting Atlas: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errs.New("network error contacting Atlas: %v", err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var o Onboarding
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, errs.New("Atlas returned invalid JSON: %v", err)
	}
	return &o, nil
}

// post sends a POST request to the given path with an optional JSON body.
func (c *Client) post(path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, errs.New("network error contacting Atlas: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errs.New("network error contacting Atlas: %v", err)
	}
	return resp, nil
}

// checkStatus returns an error for HTTP status >= 400.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode < 400 {
		return nil
	}
	raw, _ := io.ReadAll(resp.Body)
	text := string(raw)
	if len(text) > 200 {
		text = text[:200]
	}
	return errs.New("Atlas returned %d: %s", resp.StatusCode, text)
}
