package coingecko

import (
	"context"
	"encoding/json"
	"errors"
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

// newHTTPClient builds the transport stack, outermost first:
//
//	rate-limit → logging → CB → retry → response-size guard → pooled transport.
//
// The limiter sits outside logging (so latency reflects network, not throttling
// wait) and outside the breaker (so it only judges upstream health); logging
// stays above the breaker so a fast-failed request still logs.
func newHTTPClient(timeout time.Duration, cg config.CoinGeckoConfig, api *config.APIConfig, log *zerolog.Logger) *http.Client {
	res := api.OutboundResilience("coingecko")
	rl := cg.RateLimit.Settings("coingecko")
	rt := httpclient.MaxBytesReader(httpclient.SharedTransport(), maxBytes(api))
	rt = httpclient.WrapResilientTransport(rt, res)
	rt = httpclient.WrapCircuitBreaker(rt, res)
	rt = &logging.HTTPTransport{Base: rt, Logger: log, Component: "coingecko"}
	rt = httpclient.WrapRateLimited(rt, rl)
	return &http.Client{Timeout: timeout, Transport: rt, CheckRedirect: httpclient.SameHostRedirectPolicy}
}

// joinCoinPath appends "/coins/<id>/<suffix>", percent-encoding the id as ONE
// path segment. Path holds the DECODED form — an escaped string assigned to
// it alone double-encodes (% → %25); the paired RawPath keeps it correct.
func joinCoinPath(u *url.URL, coinID, suffix string) {
	rawBase := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = strings.TrimRight(u.Path, "/") + "/coins/" + coinID + "/" + suffix
	u.RawPath = rawBase + "/coins/" + url.PathEscape(coinID) + "/" + suffix
}

func maxBytes(api *config.APIConfig) int64 {
	if api == nil {
		return 0
	}
	return api.OutboundMaxResponseBytes
}

// setAPIKeyHeader picks the header the configured host expects: the Pro host
// takes x-cg-pro-api-key, the demo host x-cg-demo-api-key. The pro header on
// the demo host is rejected outright.
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
	// coinID comes from tokens.cg_id: unescaped, a value with `/`, `?` or `#`
	// would rewrite the path of a request carrying the API-key header.
	joinCoinPath(u, coinID, "market_chart/range")
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

// GetMultipleCurrencies fetches one window for many vs_currencies. Successful
// currencies are always returned and failures come back joined in the error, so
// one dead vs_currency cannot black out the rest; the map is empty only when
// all failed. The live job saves partials, the backfill treats any error as a
// failed chunk (its cursor must not skip a currency's history).
func (c *Client) GetMultipleCurrencies(ctx context.Context, coinID string, currencies []prices.Currency, from, to int64) (map[prices.Currency]*MarketChartRangeResponse, error) {
	results := make(map[prices.Currency]*MarketChartRangeResponse, len(currencies))
	var errs []error
	for _, cur := range currencies {
		data, err := c.GetMarketChartRange(ctx, coinID, string(cur), from, to)
		if err != nil {
			errs = append(errs, fmt.Errorf("currency %s: %w", cur, err))
			continue
		}
		results[cur] = data
	}
	if len(errs) > 0 {
		return results, errors.Join(errs...)
	}
	return results, nil
}
