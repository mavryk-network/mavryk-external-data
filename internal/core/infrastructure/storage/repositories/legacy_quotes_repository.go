package repositories

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// LegacyQuoteRepository serves the wide-format `/quotes` endpoint preserved
// from v0.1.0. One DB roundtrip pivots the long-format token_prices rows for
// one (token, source) into one row per timestamp with eight currency columns.
type LegacyQuoteRepository struct {
	db *gorm.DB
}

func NewLegacyQuoteRepository(db *gorm.DB) *LegacyQuoteRepository {
	return &LegacyQuoteRepository{db: db}
}

// LegacyQuoteRow is one pivoted row. Missing currencies at a given ts decode
// as zero — matching v0.1.0 wire behaviour, which serialised float64 zero
// rather than omitting the key.
type LegacyQuoteRow struct {
	Timestamp time.Time `gorm:"column:ts"`
	BTC       float64   `gorm:"column:btc"`
	USD       float64   `gorm:"column:usd"`
	EUR       float64   `gorm:"column:eur"`
	CNY       float64   `gorm:"column:cny"`
	JPY       float64   `gorm:"column:jpy"`
	KRW       float64   `gorm:"column:krw"`
	ETH       float64   `gorm:"column:eth"`
	GBP       float64   `gorm:"column:gbp"`
}

// defaultLegacyRowCap backstops an unbounded pivot on this public route. Only
// ever a fallback for limit <= 0 — never applied downward, or it would silently
// override an operator who raised server.max_query_limit.
const defaultLegacyRowCap = 10000

// QueryWide returns wide-format rows for (token, source) within [from, to],
// ts ASC, capped at limit (<= 0 falls back to defaultLegacyRowCap).
//
// Truncation drops the NEWEST rows, matching v0.1.0 and the v1 window query.
// Load-bearing: legacy clients page forward by re-issuing with `from` just past
// the last ts seen, which only advances while truncation keeps the old end.
//
// FILTER aggregation collapses the 8 long rows per ts into one wide row in
// Postgres — no pivot work in Go.
func (r *LegacyQuoteRepository) QueryWide(
	ctx context.Context,
	tokenSymbol string,
	sourceCode string,
	from time.Time,
	to time.Time,
	limit int,
) ([]LegacyQuoteRow, error) {
	const sql = `
SELECT
  ts,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'btc'))::float8, 0) AS btc,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'usd'))::float8, 0) AS usd,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'eur'))::float8, 0) AS eur,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'cny'))::float8, 0) AS cny,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'jpy'))::float8, 0) AS jpy,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'krw'))::float8, 0) AS krw,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'eth'))::float8, 0) AS eth,
  COALESCE((MAX(price) FILTER (WHERE quote_currency = 'gbp'))::float8, 0) AS gbp
FROM token_prices
WHERE token_symbol = ? AND source_code = ?
  AND ts >= ? AND ts <= ?
GROUP BY ts
ORDER BY ts ASC
`
	if limit <= 0 {
		limit = defaultLegacyRowCap
	}
	query := sql + " LIMIT ?"
	args := []any{tokenSymbol, sourceCode, from.UTC(), to.UTC(), limit}

	var rows []LegacyQuoteRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("legacy /quotes pivot: %w", err)
	}
	return rows, nil
}
