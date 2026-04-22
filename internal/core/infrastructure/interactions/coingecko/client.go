package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/httpclient"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
)

type MarketChartRangeResponse struct {
	Prices      [][]float64 `json:"prices"`
	MarketCaps  [][]float64 `json:"market_caps"`
	TotalVolume [][]float64 `json:"total_volumes"`
}

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	log     *zerolog.Logger
}

func NewClient(baseURL, apiKey string, timeout time.Duration, log *zerolog.Logger, api *config.APIConfig) *Client {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	lg := logging.WithComponent(log, "coingecko")
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		log:     lg,
		http:    newCoingeckoHTTPClient(timeout, api, lg),
	}
}

// newCoingeckoHTTPClient builds Client.Timeout + Transport stack:
// rate limit → (circuit breaker → retry → pooled transport).
func newCoingeckoHTTPClient(timeout time.Duration, api *config.APIConfig, log *zerolog.Logger) *http.Client {
	var apiCfg config.APIConfig
	if api != nil {
		apiCfg = *api
	}
	res := apiCfg.CoinGeckoOutboundResilience()
	rl := apiCfg.CoinGeckoRateLimit()
	rt := httpclient.WrapResilientTransport(httpclient.SharedTransport(), res)
	rt = httpclient.WrapRateLimited(rt, rl)
	rt = &logging.HTTPTransport{
		Base:      rt,
		Logger:    log,
		Component: "coingecko",
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: rt,
	}
}

func (c *Client) GetMarketChartRange(ctx context.Context, coinID, currency string, from, to int64) (*MarketChartRangeResponse, error) {
	url := fmt.Sprintf("%s/coins/%s/market_chart/range?vs_currency=%s&from=%d&to=%d",
		c.baseURL, coinID, currency, from, to)

	c.log.Debug().Str("url", url).Msg("coingecko_request")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "quotes-service/1.0")

	if c.apiKey != "" {
		req.Header.Set("x-cg-pro-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.log.Warn().Err(cerr).Msg("coingecko_response_body_close_error")
		}
	}()

	c.log.Info().Int("status", resp.StatusCode).Msg("coingecko_response")

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result MarketChartRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (c *Client) GetMultipleCurrencies(ctx context.Context, coinID string, currencies []quotes.Currency, from, to int64) (map[quotes.Currency]*MarketChartRangeResponse, error) {
	results := make(map[quotes.Currency]*MarketChartRangeResponse)

	for _, currency := range currencies {
		vs := string(currency)
		data, err := c.GetMarketChartRange(ctx, coinID, vs, from, to)
		if err != nil {
			return nil, fmt.Errorf("failed to get data for currency %s: %w", vs, err)
		}
		results[currency] = data
	}

	return results, nil
}
