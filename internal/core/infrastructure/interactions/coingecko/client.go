package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	return &http.Client{Timeout: timeout, Transport: rt, CheckRedirect: httpclient.SameHostRedirectPolicy}
}

func maxBytes(api *config.APIConfig) int64 {
	if api == nil {
		return 0
	}
	return api.OutboundMaxResponseBytes
}

// setAPIKeyHeader attaches the API key under the header CoinGecko expects for the
// configured host: the Pro host uses x-cg-pro-api-key, the free/demo host uses
// x-cg-demo-api-key. Sending the pro header to the demo host (the default
// api.coingecko.com) is rejected, and demo keys were previously unusable.
func (c *Client) setAPIKeyHeader(req *http.Request) {
	if c.apiKey == "" {
		return
	}
	header := "x-cg-demo-api-key"
	if strings.Contains(c.baseURL, "pro-api.coingecko.com") {
		header = "x-cg-pro-api-key"
	}
	req.Header.Set(header, c.apiKey)
}

// GetMarketChartRange fetches one (coin, vs_currency) pair window.
func (c *Client) GetMarketChartRange(ctx context.Context, coinID, vsCurrency string, from, to int64) (*MarketChartRangeResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid coingecko base url %q: %w", c.baseURL, err)
	}
	// Escape path/query segments: coinID comes from tokens.cg_id (operator data
	// today, but any admin/seed tooling could write it). Unescaped, a value with
	// `/`, `?`, `#` or an authority would rewrite the request path/query sent with
	// the API-key header attached.
	u.Path = strings.TrimRight(u.Path, "/") + "/coins/" + url.PathEscape(coinID) + "/market_chart/range"
	q := u.Query()
	q.Set("vs_currency", vsCurrency)
	q.Set("from", strconv.FormatInt(from, 10))
	q.Set("to", strconv.FormatInt(to, 10))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "mavryk-external-data/1.0")
	c.setAPIKeyHeader(req)

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
