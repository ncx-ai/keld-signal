package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

type Client struct {
	url   string
	token func() string
	hc    *http.Client
}

// NewClient builds a Client targeting url. token is called on every Fetch so
// a later credential rotation (e.g. creds.Token.Set) is observed without
// reconstructing the Client.
func NewClient(url string, token func() string, timeout time.Duration) *Client {
	return &Client{url: url, token: token, hc: &http.Client{Timeout: timeout}}
}

// Fetch GETs the org settings document. Errors (including a 404 on an Atlas that
// predates the endpoint) surface so the caller can keep the last-known settings.
func (c *Client) Fetch(ctx context.Context) (*Remote, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-keld-ingest-token", c.token())
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &retry.StatusError{Code: resp.StatusCode}
	}
	var r Remote
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}
