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

var ErrResponseTooLarge = errors.New("MvKT response exceeds size limit")

type Client struct {
	baseURL      string
	http         *http.Client
	supplyLimits SupplyLimits
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

// GetURL uses the shared MvKT transport and the existing 8 MiB read limit.
func (c *Client) GetURL(ctx context.Context, address string, out any) error {
	b, err := c.getJSONLimited(ctx, address, 8<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (c *Client) getJSONLimited(ctx context.Context, address string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MvKT/discovery HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, ErrResponseTooLarge
	}
	return b, nil
}
