package repositories

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// TokenChangeRepository serves the FT side of the price-change endpoint.
// Reuses the same DB handle as TokenPriceRepository — a separate type
// is used (not a method on TokenPriceRepository) so the change concern
// stays self-contained and the existing repo doesn't grow another axis.
//
// All anchor lookups read from the existing continuous aggregates
// (token_prices_1m / _1h / _1d). The "now" lookup reads from raw
// token_prices to match /latest semantics exactly (Decision #9 from
// the design doc).
type TokenChangeRepository struct {
	db *gorm.DB
}

// NewTokenChangeRepository builds the repo on the shared GORM handle.
func NewTokenChangeRepository(db *gorm.DB) *TokenChangeRepository {
	return &TokenChangeRepository{db: db}
}

// GetChange runs one SQL per request: a UNION ALL across the "now"
// branch and one anchor branch per period. Both kinds of branch read
// the SAME raw `token_prices` table (Decision #18) so `now` and anchor
// have symmetric semantics — both are "latest sample at-or-before the
// reference timestamp". CAs are not used here; the existing PK index
// `(token_symbol, source_code, quote_currency, ts)` makes each branch
// a single index seek (DISTINCT ON ... ORDER BY ts DESC).
//
// Row count is bounded by len(currencies) × (1 + len(periods)).
// All filter values flow through prepared-statement parameters; no
// user-controlled string ever touches the SQL body.
func (r *TokenChangeRepository) GetChange(ctx context.Context, q apiprices.ChangeQuery) (apiprices.ChangeRepoResult, error) {
	if len(q.Currencies) == 0 {
		// Defensive: ChangeService.preflight rejects an empty currency set. If we
		// get here it's a programming error; return empty rather than blowing up
		// SQL with `quote_currency IN ()`.
		//
		// NOTE: an empty Periods slice is legitimate — the service issues a
		// "now-only" refresh (Currencies non-empty, Periods empty) when only the
		// 'now' cache slot expired. buildTokenChangeSQL emits just the 'now'
		// branch in that case, so we must NOT early-return here (doing so returned
		// no 'now' row and rendered now=null every time the now-TTL lapsed).
		return apiprices.ChangeRepoResult{}, nil
	}

	sql, args, err := buildTokenChangeSQL(q)
	if err != nil {
		return apiprices.ChangeRepoResult{}, err
	}

	var rows []tokenChangeRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return apiprices.ChangeRepoResult{}, fmt.Errorf("token_change query: %w", err)
	}

	return assembleTokenChangeResult(q, rows), nil
}

// Compile-time check.
var _ apiprices.ChangeRepository = (*TokenChangeRepository)(nil)

// tokenChangeRow is the wire-compat row for the SELECT in GetChange.
// Period carries either "now" or a Period.String() value; the assembler
// switches on it to fill the right slot in ChangeRepoResult.
type tokenChangeRow struct {
	Period        string          `gorm:"column:period"`
	QuoteCurrency string          `gorm:"column:quote_currency"`
	ObservedTS    time.Time       `gorm:"column:observed_ts"`
	Price         decimal.Decimal `gorm:"column:price"`
}

const nowBranchLabel = "now"

// buildTokenChangeSQL assembles the SELECT body and the corresponding
// argument slice. Exposed for unit testing — the SQL string is
// deterministic for a given query so test assertions can pin it.
func buildTokenChangeSQL(q apiprices.ChangeQuery) (string, []any, error) {
	inFrag := strings.Repeat("?,", len(q.Currencies)-1) + "?"
	curArgs := make([]any, 0, len(q.Currencies))
	for _, c := range q.Currencies {
		curArgs = append(curArgs, c)
	}

	var sql strings.Builder
	args := make([]any, 0, 4*(1+len(q.Periods))+len(q.Currencies)*(1+len(q.Periods)))

	// "now" branch — raw token_prices, latest per currency.
	sql.WriteString(fmt.Sprintf(`SELECT '%s'::text AS period, quote_currency, observed_ts, price
FROM (
    SELECT DISTINCT ON (quote_currency)
           quote_currency,
           ts          AS observed_ts,
           price
      FROM token_prices
     WHERE token_symbol = ? AND source_code = ? AND quote_currency IN (%s)
     ORDER BY quote_currency, ts DESC
) n`, nowBranchLabel, inFrag))
	args = append(args, q.EntityKey, string(q.Source))
	args = append(args, curArgs...)

	// One UNION ALL branch per period — same raw table, different ts window
	// per Decision #18. hi = now-period (at-or-before semantics);
	// lo = now-period-staleness (prevents pulling year-old anchors).
	for _, period := range q.Periods {
		lo, hi, ok := period.AnchorWindow(q.Now)
		if !ok {
			return "", nil, fmt.Errorf("period %q has no anchor window", period)
		}
		sql.WriteString(fmt.Sprintf(`
UNION ALL
SELECT ?::text, quote_currency, observed_ts, price
FROM (
    SELECT DISTINCT ON (quote_currency)
           quote_currency,
           ts          AS observed_ts,
           price
      FROM token_prices
     WHERE token_symbol = ? AND source_code = ? AND quote_currency IN (%s)
       AND ts >= ? AND ts <= ?
     ORDER BY quote_currency, ts DESC
) a`, inFrag))
		args = append(args, string(period), q.EntityKey, string(q.Source))
		args = append(args, curArgs...)
		args = append(args, lo.UTC(), hi.UTC())
	}

	return sql.String(), args, nil
}

// assembleTokenChangeResult turns flat row dump into the application-layer
// ChangeRepoResult. Currencies absent from a branch's rows produce
// Found=false entries so the service can render JSON null per Decision #3.
//
// Pure function — exposed for unit testing.
func assembleTokenChangeResult(q apiprices.ChangeQuery, rows []tokenChangeRow) apiprices.ChangeRepoResult {
	// Index incoming rows by (period, currency) for O(1) lookup.
	type key struct {
		Period   string
		Currency string
	}
	idx := make(map[key]tokenChangeRow, len(rows))
	for _, r := range rows {
		idx[key{Period: r.Period, Currency: r.QuoteCurrency}] = r
	}

	now := make([]apiprices.ChangeNow, 0, len(q.Currencies))
	for _, c := range q.Currencies {
		r, ok := idx[key{Period: nowBranchLabel, Currency: c}]
		if !ok {
			now = append(now, apiprices.ChangeNow{Currency: c, Found: false})
			continue
		}
		now = append(now, apiprices.ChangeNow{
			Currency: c,
			Price:    r.Price,
			TS:       r.ObservedTS.UTC(),
			Found:    true,
		})
	}

	anchors := make([]apiprices.ChangeAnchor, 0, len(q.Currencies)*len(q.Periods))
	for _, p := range q.Periods {
		for _, c := range q.Currencies {
			r, ok := idx[key{Period: string(p), Currency: c}]
			if !ok {
				anchors = append(anchors, apiprices.ChangeAnchor{
					Currency: c,
					Period:   p,
					Found:    false,
				})
				continue
			}
			anchors = append(anchors, apiprices.ChangeAnchor{
				Currency: c,
				Period:   p,
				Price:    r.Price,
				Bucket:   r.ObservedTS.UTC(),
				Found:    true,
			})
		}
	}

	return apiprices.ChangeRepoResult{Now: now, Anchors: anchors}
}

// Static check — Period whitelist is referenced; this keeps the import
// pulled in even if no other prod code uses it directly here.
var _ = prices.AllPeriods
