package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

type Client struct {
	url   func() string
	token func() string
	hc    *http.Client
}

// ErrNotPaired is what Fetch answers while url resolves to "": the machine has
// no Atlas to ask. The settings poll treats it as any other fetch error — it
// keeps the last-known effective settings and says nothing new — so a daemon
// that has not been paired yet does not report an org-settings failure.
var ErrNotPaired = errors.New("this machine is not paired with Atlas yet (no endpoint in ~/.keld/hook.json)")

// NewClient builds a Client targeting url. token is called on every Fetch so
// a later credential rotation (e.g. creds.Token.Set) is observed without
// reconstructing the Client.
func NewClient(url string, token func() string, timeout time.Duration) *Client {
	return NewDeferredClient(func() string { return url }, token, timeout)
}

// NewDeferredClient resolves the endpoint on every Fetch, so the client can be
// constructed before the machine is paired — the same treatment token already
// has, for the same reason.
func NewDeferredClient(url func() string, token func() string, timeout time.Duration) *Client {
	return &Client{url: url, token: token, hc: &http.Client{Timeout: timeout}}
}

// Fetch GETs the org settings document. Errors (including a 404 on an Atlas that
// predates the endpoint) surface so the caller can keep the last-known settings.
func (c *Client) Fetch(ctx context.Context) (*Remote, error) {
	url := strings.TrimSpace(c.url())
	if url == "" {
		return nil, ErrNotPaired
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
