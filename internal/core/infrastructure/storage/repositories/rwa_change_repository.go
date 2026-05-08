package repositories

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// RWAChangeRepository serves the RWA side of the price-change endpoint.
// Mirrors TokenChangeRepository in shape but the SQL filter axes differ:
//
//	FT  → (token_symbol, source_code, quote_currency)
//	RWA → (pair_id, side)
//
// rwa_quote_prices CAs are not keyed by currency — the `currency` axis
// in ChangeQuery for the RWA path is a single-entry slice carrying the
// pair's native quote ticker as metadata only. The repo SQL ignores
// the currency value; the assembler echoes it back into ChangeRepoResult
// so the service-level shape stays uniform across FT and RWA.
//
// EntityKey is the decimal pair_id; AuxKey is the orderbook side
// ("last" — the only side surfaced on the read path).
type RWAChangeRepository struct {
	db *gorm.DB
}

// NewRWAChangeRepository builds the repo on the shared GORM handle.
func NewRWAChangeRepository(db *gorm.DB) *RWAChangeRepository {
	return &RWAChangeRepository{db: db}
}

// GetChange runs one SQL per request: a UNION ALL across the "now"
// branch (raw rwa_quote_prices) plus one anchor branch per period
// (rwa_quote_prices_{1m|1h|1d}). Each branch returns at most one row
// (DISTINCT ON (side) — but the metric filter is pinned to a single side,
// so it's effectively `LIMIT 1`).
func (r *RWAChangeRepository) GetChange(ctx context.Context, q apiprices.ChangeQuery) (apiprices.ChangeRepoResult, error) {
	if len(q.Currencies) == 0 || len(q.Periods) == 0 {
		return apiprices.ChangeRepoResult{}, nil
	}
	if len(q.Currencies) != 1 {
		// RWA path expects a single-element currency slice (the pair's
		// native quote, used as metadata). More than one is a programming
		// error in the handler.
		return apiprices.ChangeRepoResult{}, coreerrors.InvalidArgument(
			"RWA change request must have exactly one currency (the pair's native quote)")
	}
	pid, err := strconv.ParseInt(q.EntityKey, 10, 64)
	if err != nil {
		return apiprices.ChangeRepoResult{}, coreerrors.InvalidArgument(
			"EntityKey must be a numeric pair_id, got: " + q.EntityKey)
	}
	side := strings.TrimSpace(q.AuxKey)
	if side == "" {
		return apiprices.ChangeRepoResult{}, coreerrors.InvalidArgument(
			`AuxKey (side) is required, e.g. "last"`)
	}

	sql, args, err := buildRWAChangeSQL(q, pid, side)
	if err != nil {
		return apiprices.ChangeRepoResult{}, err
	}

	var rows []rwaChangeRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return apiprices.ChangeRepoResult{}, fmt.Errorf("rwa_change query: %w", err)
	}

	return assembleRWAChangeResult(q, rows), nil
}

// Compile-time check.
var _ apiprices.ChangeRepository = (*RWAChangeRepository)(nil)

// rwaChangeRow is the wire-compat row for the SELECT in GetChange.
// No currency column — RWA storage isn't keyed by currency.
type rwaChangeRow struct {
	Period     string          `gorm:"column:period"`
	ObservedTS time.Time       `gorm:"column:observed_ts"`
	Price      decimal.Decimal `gorm:"column:price"`
}

// buildRWAChangeSQL assembles the SELECT body and the corresponding
// argument slice for the RWA path. Mirrors buildTokenChangeSQL in shape
// but with the different filter axes. Exposed for unit testing.
func buildRWAChangeSQL(q apiprices.ChangeQuery, pid int64, side string) (string, []any, error) {
	var sql strings.Builder
	args := make([]any, 0, 4*(1+len(q.Periods)))

	// "now" branch — raw rwa_quote_prices, freshest row for (pair, side).
	sql.WriteString(fmt.Sprintf(`SELECT '%s'::text AS period, observed_ts, price
FROM (
    SELECT ts AS observed_ts, price
      FROM rwa_quote_prices
     WHERE pair_id = ? AND side = ?
     ORDER BY ts DESC
     LIMIT 1
) n`, nowBranchLabel))
	args = append(args, pid, side)

	for _, period := range q.Periods {
		ca := period.BackingCA()
		if ca == "" {
			return "", nil, fmt.Errorf("period %q has no backing CA — preflight should have caught this", period)
		}
		lo, hi, ok := period.ToleranceWindow(q.Now)
		if !ok {
			return "", nil, fmt.Errorf("period %q has no tolerance window", period)
		}
		view := "rwa_quote_prices" + ca // closed enum from Period.BackingCA — safe to interpolate
		sql.WriteString(fmt.Sprintf(`
UNION ALL
SELECT ?::text, bucket AS observed_ts, close_price AS price
FROM (
    SELECT bucket, close_price
      FROM %s
     WHERE pair_id = ? AND side = ?
       AND bucket >= ? AND bucket <= ?
     ORDER BY bucket DESC
     LIMIT 1
) a`, view))
		args = append(args, string(period), pid, side, lo.UTC(), hi.UTC())
	}

	return sql.String(), args, nil
}

// assembleRWAChangeResult turns flat row dump into ChangeRepoResult.
// q.Currencies always has length 1 for the RWA path; that single value
// is echoed into every emitted row so downstream service code can use a
// uniform map keyed by string.
func assembleRWAChangeResult(q apiprices.ChangeQuery, rows []rwaChangeRow) apiprices.ChangeRepoResult {
	currency := q.Currencies[0]
	idx := make(map[string]rwaChangeRow, len(rows))
	for _, r := range rows {
		idx[r.Period] = r
	}

	now := []apiprices.ChangeNow{{Currency: currency}}
	if r, ok := idx[nowBranchLabel]; ok {
		now[0].Price = r.Price
		now[0].TS = r.ObservedTS.UTC()
		now[0].Found = true
	}

	anchors := make([]apiprices.ChangeAnchor, 0, len(q.Periods))
	for _, p := range q.Periods {
		anc := apiprices.ChangeAnchor{Currency: currency, Period: p}
		if r, ok := idx[string(p)]; ok {
			anc.Price = r.Price
			anc.Bucket = r.ObservedTS.UTC()
			anc.Found = true
		}
		anchors = append(anchors, anc)
	}

	return apiprices.ChangeRepoResult{Now: now, Anchors: anchors}
}

// Static check — Period whitelist reference.
var _ = prices.AllPeriods
