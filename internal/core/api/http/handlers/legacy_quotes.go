package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/gin-gonic/gin"
)

// LegacyQuoteFetcher abstracts the wide-pivot store so handler tests need no DB.
type LegacyQuoteFetcher interface {
	QueryWide(
		ctx context.Context,
		tokenSymbol string,
		sourceCode string,
		from time.Time,
		to time.Time,
		limit int,
	) ([]repositories.LegacyQuoteRow, error)
}

// LegacyQuotesDeps wires the legacy `/quotes` handler. Token + source are fixed
// to MVRK + CoinGecko (v0.1.0 behaviour); everything else routes through v1.
type LegacyQuotesDeps struct {
	Repo        LegacyQuoteFetcher
	TokenSymbol string // "mvrk"
	SourceCode  string // "coingecko"
	MaxLimit    int    // server.max_query_limit
}

// legacyQuoteOut mirrors the v0.1.0 wire shape exactly: lowercase currency keys,
// JSON numbers (no quoting), UTC RFC3339 timestamp.
type legacyQuoteOut struct {
	Timestamp string  `json:"timestamp"`
	BTC       float64 `json:"btc"`
	USD       float64 `json:"usd"`
	EUR       float64 `json:"eur"`
	CNY       float64 `json:"cny"`
	JPY       float64 `json:"jpy"`
	KRW       float64 `json:"krw"`
	ETH       float64 `json:"eth"`
	GBP       float64 `json:"gbp"`
}

// LegacyQuotes — GET /quotes
//
// Drop-in restoration of the v0.1.0 endpoint: MVRK quotes from CoinGecko in wide
// format, keeping the legacy `{"error": "..."}` envelope.
//
// Query params:
//   - from  RFC3339, default now-24h
//   - to    RFC3339, default now
//   - limit positive int, capped by server.max_query_limit and defaulted to it
//
// The window is unbounded, matching v0.1.0: an over-wide window is answered with
// the capped row set, never rejected.
func (d LegacyQuotesDeps) LegacyQuotes() gin.HandlerFunc {
	return func(c *gin.Context) {
		fromStr := c.Query("from")
		toStr := c.Query("to")
		limitStr := c.Query("limit")

		now := time.Now()
		from := now.Add(-24 * time.Hour)
		to := now

		if fromStr != "" {
			t, err := time.Parse(time.RFC3339, fromStr)
			if err != nil {
				legacyError(c, http.StatusBadRequest,
					"Invalid 'from' parameter format. Use RFC3339 format (e.g., 2023-01-01T00:00:00Z)")
				return
			}
			from = t
		}
		if toStr != "" {
			t, err := time.Parse(time.RFC3339, toStr)
			if err != nil {
				legacyError(c, http.StatusBadRequest,
					"Invalid 'to' parameter format. Use RFC3339 format (e.g., 2023-01-01T00:00:00Z)")
				return
			}
			to = t
		}
		if from.After(to) {
			legacyError(c, http.StatusBadRequest,
				"Invalid time range: 'from' must be before 'to'")
			return
		}

		limit := 0
		if limitStr != "" {
			parsed, err := strconv.Atoi(limitStr)
			if err != nil || parsed <= 0 {
				legacyError(c, http.StatusBadRequest,
					"Invalid 'limit' parameter. Must be a positive integer")
				return
			}
			if d.MaxLimit > 0 && parsed > d.MaxLimit {
				legacyError(c, http.StatusBadRequest,
					"Invalid 'limit' parameter: exceeds maximum")
				return
			}
			limit = parsed
		}
		// Always window mode here, so an absent ?limit falls back to the server
		// cap, keeping server.max_query_limit authoritative over the repository's
		// defaultLegacyRowCap backstop.
		if limit == 0 && d.MaxLimit > 0 {
			limit = d.MaxLimit
		}

		rows, err := d.Repo.QueryWide(c.Request.Context(), d.TokenSymbol, d.SourceCode, from, to, limit)
		if err != nil {
			// Never leak the raw repository error (SQL fragments, table names,
			// connection diagnostics) to unauthenticated callers: log it
			// server-side and return a static message.
			_ = c.Error(err)
			legacyError(c, http.StatusInternalServerError, "Failed to get quotes")
			return
		}

		out := make([]legacyQuoteOut, len(rows))
		for i, r := range rows {
			out[i] = legacyQuoteOut{
				Timestamp: r.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				BTC:       r.BTC,
				USD:       r.USD,
				EUR:       r.EUR,
				CNY:       r.CNY,
				JPY:       r.JPY,
				KRW:       r.KRW,
				ETH:       r.ETH,
				GBP:       r.GBP,
			}
		}

		// json.Marshal directly, so a zero-length slice serialises as `[]` per
		// the legacy contract regardless of gin's defaults.
		body, err := json.Marshal(out)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode response"})
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
	}
}

func legacyError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"error": msg})
}
