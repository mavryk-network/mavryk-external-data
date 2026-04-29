package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/httpclient"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
)

// MarketChartRangeResponse mirrors the CoinGecko /coins/{id}/market_chart/range payload.
type MarketChartRangeResponse struct {
	Prices      [][]float64 `json:"prices"`
	MarketCaps  [][]float64 `json:"market_caps"`
	TotalVolume [][]float64 `json:"total_volumes"`
}

// Client is a thin REST client over CoinGecko market_chart/range.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	log     *zerolog.Logger
}

// NewClient builds a CoinGecko HTTP client. Per-service rate limit is shared via
// the "coingecko" registry key; retry + circuit breaker come from the API config.
func NewClient(cg config.CoinGeckoConfig, api *config.APIConfig, timeout time.Duration, log *zerolog.Logger) *Client {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	lg := logging.WithComponent(log, "coingecko")
	return &Client{
		baseURL: cg.BaseURL,
		apiKey:  cg.APIKey,
		log:     lg,
		http:    newHTTPClient(timeout, cg, api, lg),
	}
}

// newHTTPClient constructs the resilient transport stack. Refactoring v2 §3.4
// — the rate-limiter sits OUTSIDE the logging transport so log latency reflects
// only network time, not throttling wait. Order (outermost first):
//
//	rate-limit → logging → retry/CB → response-size guard → pooled transport.
func newHTTPClient(timeout time.Duration, cg config.CoinGeckoConfig, api *config.APIConfig, log *zerolog.Logger) *http.Client {
	res := api.OutboundResilience("coingecko")
	rl := cg.RateLimit.Settings("coingecko")
	rt := httpclient.MaxBytesReader(httpclient.SharedTransport(), maxBytes(api))
	rt = httpclient.WrapResilientTransport(rt, res)
	rt = &logging.HTTPTransport{Base: rt, Logger: log, Component: "coingecko"}
	rt = httpclient.WrapRateLimited(rt, rl)
	return &http.Client{Timeout: timeout, Transport: rt}
}

func maxBytes(api *config.APIConfig) int64 {
	if api == nil {
		return 0
	}
	return api.OutboundMaxResponseBytes
}

// GetMarketChartRange fetches one (coin, vs_currency) pair window.
func (c *Client) GetMarketChartRange(ctx context.Context, coinID, vsCurrency string, from, to int64) (*MarketChartRangeResponse, error) {
	url := fmt.Sprintf("%s/coins/%s/market_chart/range?vs_currency=%s&from=%d&to=%d",
		c.baseURL, coinID, vsCurrency, from, to)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "mavryk-external-data/1.0")
	if c.apiKey != "" {
		req.Header.Set("x-cg-pro-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.log.Warn().Err(cerr).Msg("coingecko_response_body_close_error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coingecko returned status %d", resp.StatusCode)
	}

	var result MarketChartRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// GetMultipleCurrencies fetches one window for many vs_currencies. Returns the
// first error and stops; partial results are dropped (caller logs and retries).
func (c *Client) GetMultipleCurrencies(ctx context.Context, coinID string, currencies []prices.Currency, from, to int64) (map[prices.Currency]*MarketChartRangeResponse, error) {
	results := make(map[prices.Currency]*MarketChartRangeResponse, len(currencies))
	for _, cur := range currencies {
		data, err := c.GetMarketChartRange(ctx, coinID, string(cur), from, to)
		if err != nil {
			return nil, fmt.Errorf("currency %s: %w", cur, err)
		}
		results[cur] = data
	}
	return results, nil
}
