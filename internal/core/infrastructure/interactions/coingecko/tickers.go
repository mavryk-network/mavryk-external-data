package coingecko

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"

	"github.com/shopspring/decimal"
)

// TickersResponse mirrors the CoinGecko GET /coins/{id}/tickers payload.
// Fields we don't read are omitted (CG returns many — coin id, page, links).
//
// `Market.Logo` only appears with `include_exchange_logo=true` (CG Pro flag).
type TickersResponse struct {
	Tickers []TickerEntry `json:"tickers"`
}

type TickerEntry struct {
	Base                   string                     `json:"base"`
	Target                 string                     `json:"target"`
	Market                 TickerMarket               `json:"market"`
	Last                   *decimal.Decimal           `json:"last"`
	Volume                 *decimal.Decimal           `json:"volume"`
	ConvertedLast          map[string]decimal.Decimal `json:"converted_last"`
	ConvertedVolume        map[string]decimal.Decimal `json:"converted_volume"`
	TrustScore             string                     `json:"trust_score"`
	BidAskSpreadPercentage *decimal.Decimal           `json:"bid_ask_spread_percentage"`
	Timestamp              string                     `json:"timestamp"`      // RFC3339, optional
	LastTradedAt           string                     `json:"last_traded_at"` // RFC3339, optional
	LastFetchAt            string                     `json:"last_fetch_at"`  // RFC3339, optional
	IsAnomaly              bool                       `json:"is_anomaly"`
	IsStale                bool                       `json:"is_stale"`
	TradeURL               string                     `json:"trade_url"`
	TokenInfoURL           string                     `json:"token_info_url"`
}

type TickerMarket struct {
	Name                string `json:"name"`
	Identifier          string `json:"identifier"`
	HasTradingIncentive bool   `json:"has_trading_incentive"`
	Logo                string `json:"logo"` // CG Pro flag include_exchange_logo
}

// GetTickers fetches per-exchange tickers for `coinID` (e.g. "mavryk-network").
// Pagination not implemented in v1 — single page is enough for MVRK in
// foreseeable future. When MVRK is on >100 exchanges, page through `?page=1..`.
func (c *Client) GetTickers(ctx context.Context, coinID string, includeLogo bool) (*TickersResponse, error) {
	url := fmt.Sprintf("%s/coins/%s/tickers", c.baseURL, coinID)
	if includeLogo {
		url += "?include_exchange_logo=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create tickers request: %w", err)
	}
	req.Header.Set("User-Agent", "mavryk-external-data/1.0")
	if c.apiKey != "" {
		req.Header.Set("x-cg-pro-api-key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute tickers request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			c.log.Warn().Err(cerr).Msg("coingecko_tickers_body_close_error")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coingecko tickers returned status %d", resp.StatusCode)
	}
	var out TickersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode tickers: %w", err)
	}
	return &out, nil
}

// MapToTickers converts a CoinGecko tickers response into the domain shape used
// by the storage repository.
//
// Skips rows where:
//   - market.identifier is empty (no FK target)
//   - last is missing or non-positive (can't compute change% or render in UI)
//   - base symbol doesn't match the expected token (defensive — CG /tickers
//     occasionally returns entries where the requested coin is the QUOTE, not
//     the base; we don't want to store MVRK as a quote against another token)
//
// `now` is injected so the job's deterministic tick-time is what we record,
// not a wall-clock spread across mapping (helps testing too). When CG provides
// last_fetch_at we prefer that; otherwise last_traded_at; otherwise now.
func MapToTickers(
	resp *TickersResponse,
	source prices.Source,
	token prices.Token,
	now time.Time,
) ([]tickers.Exchange, []tickers.Ticker) {
	if resp == nil || len(resp.Tickers) == 0 {
		return nil, nil
	}
	tokenStr := strings.ToLower(strings.TrimSpace(string(token)))
	now = now.UTC()

	exMap := make(map[string]tickers.Exchange, len(resp.Tickers))
	rows := make([]tickers.Ticker, 0, len(resp.Tickers))

	for _, e := range resp.Tickers {
		id := strings.TrimSpace(e.Market.Identifier)
		if id == "" {
			continue
		}
		base := strings.ToLower(strings.TrimSpace(e.Base))
		// Match by symbol OR by token contract address (some CG entries report
		// `base` as the on-chain token address rather than the symbol — accept
		// either to maximize coverage).
		if base != tokenStr && !strings.EqualFold(base, string(token)) {
			// Skip rows where MVRK is the quote, not the base. We could
			// support both directions later, but the UI semantics differ
			// ("price of MVRK in X" vs "price of X in MVRK").
			continue
		}
		if e.Last == nil || e.Last.IsZero() || e.Last.IsNegative() {
			continue
		}

		ts := pickTickerTimestamp(e, now)

		// Exchange — UPSERT row. Keep the latest name/logo we see, fall back
		// to the identifier if name is missing.
		name := strings.TrimSpace(e.Market.Name)
		if name == "" {
			name = id
		}
		exMap[id] = tickers.Exchange{
			ID:                  id,
			Name:                name,
			LogoURL:             strings.TrimSpace(e.Market.Logo),
			Kind:                tickers.ClassifyExchangeKind(id),
			HasTradingIncentive: e.Market.HasTradingIncentive,
			LastSeenAt:          now,
		}

		target := strings.ToLower(strings.TrimSpace(e.Target))
		row := tickers.Ticker{
			Token:        token,
			Source:       source,
			ExchangeID:   id,
			TargetSymbol: target,
			Timestamp:    ts,
			LastPrice:    *e.Last,
			VolumeBase:   e.Volume,
			BidAskSpread: e.BidAskSpreadPercentage,
			TrustScore:   strings.ToLower(strings.TrimSpace(e.TrustScore)),
			IsAnomaly:    e.IsAnomaly || e.IsStale, // treat stale-from-CG as anomaly too
			TradeURL:     strings.TrimSpace(e.TradeURL),
		}
		if tt := parseTickerTime(e.LastTradedAt); !tt.IsZero() {
			row.LastTradedAt = tt
		}
		rows = append(rows, row)
	}

	exchanges := make([]tickers.Exchange, 0, len(exMap))
	for _, e := range exMap {
		exchanges = append(exchanges, e)
	}
	return exchanges, rows
}

// pickTickerTimestamp returns the freshest CG-provided timestamp, falling back
// to `now`. last_fetch_at is the strongest signal (when CG observed the row);
// last_traded_at is best-effort (depends on exchange API). `timestamp` (CG's
// per-ticker observation time) is used if both fetch and trade are missing.
func pickTickerTimestamp(e TickerEntry, now time.Time) time.Time {
	if t := parseTickerTime(e.LastFetchAt); !t.IsZero() {
		return t
	}
	if t := parseTickerTime(e.Timestamp); !t.IsZero() {
		return t
	}
	if t := parseTickerTime(e.LastTradedAt); !t.IsZero() {
		return t
	}
	return now
}

func parseTickerTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
