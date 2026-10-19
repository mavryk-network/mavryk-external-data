// Package mvkt provides the shared, bounded MvKT HTTP connection.
package mvkt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/httpclient"
)

var ErrResponseTooLarge = errors.New("MvKT response exceeds 8 MiB")

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(cfg config.MvKTConfig, api *config.APIConfig) *Client {
	baseURL := cfg.IndexerURL
	if baseURL == "" {
		baseURL = config.DefaultMvKTIndexerURL
	}
	transport := httpclient.WrapResilientTransport(httpclient.SharedTransport(), api.OutboundResilience("mvkt"))
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Transport: transport, Timeout: 15 * time.Second}}
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.GetURL(ctx, c.baseURL+path, out)
}

// GetURL also serves the fixed Basenet contract allowlist over the same transport.
func (c *Client) GetURL(ctx context.Context, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MvKT/discovery HTTP %d", resp.StatusCode)
	}
	const maxBytes = 8 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxBytes {
		return ErrResponseTooLarge
	}
	return json.Unmarshal(b, out)
}
