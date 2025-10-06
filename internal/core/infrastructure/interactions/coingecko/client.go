package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
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
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) GetMarketChartRange(ctx context.Context, currency string, from, to int64) (*MarketChartRangeResponse, error) {
	url := fmt.Sprintf("%s/coins/mavryk-network/market_chart/range?vs_currency=%s&from=%d&to=%d",
		c.baseURL, currency, from, to)

	log.Printf("CoinGecko API Request: %s", url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "quotes-service/1.0")

	// Add API key if provided
	if c.apiKey != "" {
		req.Header.Set("x-cg-pro-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("error closing response body: %v", cerr)
		}
	}()

	log.Printf("CoinGecko API Response: Status %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var result MarketChartRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (c *Client) GetMultipleCurrencies(ctx context.Context, currencies []string, from, to int64) (map[string]*MarketChartRangeResponse, error) {
	results := make(map[string]*MarketChartRangeResponse)

	for _, currency := range currencies {
		data, err := c.GetMarketChartRange(ctx, currency, from, to)
		if err != nil {
			return nil, fmt.Errorf("failed to get data for currency %s: %w", currency, err)
		}
		results[currency] = data
	}

	return results, nil
}
