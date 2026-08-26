package repositories

import (
	"context"
	"fmt"
	"strings"
	"time"

	apitickers "quotes/internal/core/application/tickers"
	"quotes/internal/core/domain/tickers"
	"quotes/internal/core/infrastructure/storage/entities"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TickerRepository serves long-format per-exchange ticker data.
//
// Two physical tables back this:
//   - `exchanges`     — small lookup, UPSERT on every job tick.
//   - `token_tickers` — hypertable, INSERT ON CONFLICT DO NOTHING (idempotent
//     under repeat ticks at the same `ts`).
//
// SaveSnapshot writes BOTH inside one transaction — the FK invariant on
// `token_tickers.exchange_id` requires exchanges to land before any tickers
// referencing a brand-new exchange.
type TickerRepository struct {
	db        *gorm.DB
	batchSize int
}

func NewTickerRepository(db *gorm.DB) *TickerRepository {
	return &TickerRepository{db: db, batchSize: DefaultBatchSize}
}

// WithBatchSize sets the CreateInBatches chunk size (mutates in place;
// returned for chainability). n <= 0 falls back to DefaultBatchSize.
func (r *TickerRepository) WithBatchSize(n int) *TickerRepository {
	if n > 0 {
		r.batchSize = n
	}
	return r
}

func exchangeUpsert() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name", "logo_url", "kind", "has_trading_incentive", "last_seen_at",
		}),
	}
}

func tickerInsert() clause.OnConflict {
	// Idempotent re-ingest under the 5-min cadence — if the upstream returns
	// the same row twice (we never lower granularity below the CG cadence),
	// keep the existing row. Numbers don't get rewritten on conflict.
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "token_symbol"}, {Name: "source_code"},
			{Name: "exchange_id"}, {Name: "target_symbol"}, {Name: "ts"},
		},
		DoNothing: true,
	}
}

// SaveSnapshot writes exchanges (UPSERT) and tickers (INSERT) in one tx.
func (r *TickerRepository) SaveSnapshot(
	ctx context.Context,
	exchanges []tickers.Exchange,
	rows []tickers.Ticker,
) (int64, error) {
	if len(exchanges) == 0 && len(rows) == 0 {
		return 0, nil
	}
	var total int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(exchanges) > 0 {
			exRows := exchangesToEntities(exchanges)
			res := tx.Clauses(exchangeUpsert()).CreateInBatches(exRows, r.batchSize)
			if res.Error != nil {
				return fmt.Errorf("upsert exchanges: %w", res.Error)
			}
			// exchanges upsert count is not part of the ticker delta — keep total
			// pinned to ticker inserts for cache-invalidation accounting.
		}
		if len(rows) > 0 {
			tRows := tickersToEntities(rows)
			res := tx.Clauses(tickerInsert()).CreateInBatches(tRows, r.batchSize)
			if res.Error != nil {
				return fmt.Errorf("insert token_tickers: %w", res.Error)
			}
			total = res.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// LatestSnapshot returns one row per (exchange, target) for `token`, freshest
// first, with 1D change% joined in via LATERAL against the same table.
//
// Query shape:
//
//	WITH latest AS (
//	    SELECT DISTINCT ON (exchange_id, target_symbol) *
//	      FROM token_tickers
//	     WHERE token_symbol = ? AND source_code = ?
//	     [AND ts >= now - stale_after]
//	     ORDER BY exchange_id, target_symbol, ts DESC
//	)
//	SELECT latest.*, ago.last_price AS price_24h_ago,
//	       exchanges.name AS exchange_name, exchanges.logo_url, exchanges.kind
//	  FROM latest
//	  JOIN exchanges ON exchanges.id = latest.exchange_id
//	  LEFT JOIN LATERAL (
//	     SELECT last_price FROM token_tickers
//	      WHERE token_symbol = latest.token_symbol
//	        AND source_code  = latest.source_code
//	        AND exchange_id  = latest.exchange_id
//	        AND target_symbol = latest.target_symbol
//	        AND ts <= latest.ts - INTERVAL '24 hours'
//	        AND ts >= latest.ts - INTERVAL '25 hours'
//	      ORDER BY ts DESC LIMIT 1
//	  ) ago ON true
//	  ORDER BY COALESCE(latest.volume_24h_base, 0) DESC;
//
// Ordering uses base-token volume alone (same unit for every row); the old
// last_price × volume product mixed quote units, ranking a BTC-quoted market
// ~60000× below a USDT one at equal real volume.
//
// The anchor bracket [ts-25h, ts-24h] mirrors Period.AnchorWindow's 1h budget:
// without the lower bound an ingestion gap would anchor "24h ago" on a row
// arbitrarily far back and report a multi-week move as a 24h change. No row in
// the bracket → change_24h_pct is null.
//
// All filters parameterised. is_stale derived in Go from the returned `ts`
// against the caller's StaleAfter window.
func (r *TickerRepository) LatestSnapshot(
	ctx context.Context,
	q apitickers.LatestQuery,
) (tickers.Snapshot, error) {
	stalePredicate := ""
	args := []any{string(q.Token), string(q.Source)}
	if !q.IncludeStale && q.StaleAfter > 0 {
		stalePredicate = " AND ts >= ?"
		args = append(args, time.Now().UTC().Add(-q.StaleAfter))
	}

	sql := `
WITH latest AS (
    SELECT DISTINCT ON (exchange_id, target_symbol)
           token_symbol, source_code, exchange_id, target_symbol, ts,
           last_price, volume_24h_base, bid_ask_spread_pct, trust_score,
           is_anomaly, trade_url, last_traded_at
      FROM token_tickers
     WHERE token_symbol = ? AND source_code = ?` + stalePredicate + `
     ORDER BY exchange_id, target_symbol, ts DESC
)
SELECT
    latest.exchange_id   AS exchange_id,
    latest.target_symbol AS target_symbol,
    latest.ts            AS ts,
    latest.last_price    AS last_price,
    latest.volume_24h_base AS volume_24h_base,
    latest.bid_ask_spread_pct AS bid_ask_spread_pct,
    latest.trust_score   AS trust_score,
    latest.is_anomaly    AS is_anomaly,
    latest.trade_url     AS trade_url,
    latest.last_traded_at AS last_traded_at,
    ago.last_price       AS price_24h_ago,
    e.name               AS exchange_name,
    e.logo_url           AS exchange_logo,
    e.kind               AS exchange_kind,
    e.has_trading_incentive AS exchange_has_trading_incentive
  FROM latest
  JOIN exchanges e ON e.id = latest.exchange_id
  LEFT JOIN LATERAL (
      SELECT last_price FROM token_tickers
       WHERE token_symbol  = latest.token_symbol
         AND source_code   = latest.source_code
         AND exchange_id   = latest.exchange_id
         AND target_symbol = latest.target_symbol
         AND ts <= latest.ts - INTERVAL '24 hours'
         AND ts >= latest.ts - INTERVAL '25 hours'
       ORDER BY ts DESC LIMIT 1
  ) ago ON TRUE
  ORDER BY COALESCE(latest.volume_24h_base, 0) DESC NULLS LAST,
           latest.exchange_id ASC, latest.target_symbol ASC`

	type row struct {
		ExchangeID                  string           `gorm:"column:exchange_id"`
		TargetSymbol                string           `gorm:"column:target_symbol"`
		Timestamp                   time.Time        `gorm:"column:ts"`
		LastPrice                   decimal.Decimal  `gorm:"column:last_price"`
		Volume24hBase               *decimal.Decimal `gorm:"column:volume_24h_base"`
		BidAskSpreadPct             *decimal.Decimal `gorm:"column:bid_ask_spread_pct"`
		TrustScore                  *string          `gorm:"column:trust_score"`
		IsAnomaly                   bool             `gorm:"column:is_anomaly"`
		TradeURL                    *string          `gorm:"column:trade_url"`
		LastTradedAt                *time.Time       `gorm:"column:last_traded_at"`
		Price24hAgo                 *decimal.Decimal `gorm:"column:price_24h_ago"`
		ExchangeName                string           `gorm:"column:exchange_name"`
		ExchangeLogo                *string          `gorm:"column:exchange_logo"`
		ExchangeKind                string           `gorm:"column:exchange_kind"`
		ExchangeHasTradingIncentive bool             `gorm:"column:exchange_has_trading_incentive"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return tickers.Snapshot{}, fmt.Errorf("query latest token_tickers: %w", err)
	}

	now := time.Now().UTC()
	var newest time.Time
	out := tickers.Snapshot{
		Token:  q.Token,
		Source: q.Source,
		Rows:   make([]tickers.SnapshotRow, 0, len(rows)),
	}
	for _, r := range rows {
		row := tickers.SnapshotRow{
			Exchange: tickers.Exchange{
				ID:                  r.ExchangeID,
				Name:                r.ExchangeName,
				LogoURL:             derefString(r.ExchangeLogo),
				Kind:                tickers.ExchangeKind(r.ExchangeKind),
				HasTradingIncentive: r.ExchangeHasTradingIncentive,
			},
			TargetSymbol: r.TargetSymbol,
			Timestamp:    r.Timestamp.UTC(),
			LastPrice:    r.LastPrice,
			VolumeBase:   r.Volume24hBase,
			BidAskSpread: r.BidAskSpreadPct,
			TrustScore:   derefString(r.TrustScore),
			IsAnomaly:    r.IsAnomaly,
			TradeURL:     derefString(r.TradeURL),
			Change24hPct: percentChange(r.LastPrice, r.Price24hAgo),
		}
		if q.StaleAfter > 0 {
			row.IsStale = now.Sub(r.Timestamp) > q.StaleAfter
		}
		if r.Timestamp.After(newest) {
			newest = r.Timestamp.UTC()
		}
		out.Rows = append(out.Rows, row)
	}
	out.Timestamp = newest
	return out, nil
}

// VolumeDistribution aggregates the freshest-per-(exchange,target) volume
// row into either an exchange grouping or a target-symbol grouping. Stale
// rows always excluded — pie charts about current state, never history.
//
// Two-stage SQL: the inner DISTINCT ON keeps "freshest row per pair", the
// outer GROUP BY sums those into the requested dimension. SUM is over
// snapshot values of `volume_24h_base` (CG returns a rolling 24h total per
// row, never a delta — so the outer aggregation is one number per pair, not
// across time).
func (r *TickerRepository) VolumeDistribution(
	ctx context.Context,
	q apitickers.DistributionQuery,
) (tickers.Distribution, error) {
	stalePredicate := ""
	args := []any{string(q.Token), string(q.Source)}
	if q.StaleAfter > 0 {
		stalePredicate = " AND ts >= ?"
		args = append(args, time.Now().UTC().Add(-q.StaleAfter))
	}

	var groupExpr, selectKey string
	switch q.GroupBy {
	case tickers.GroupByExchange:
		groupExpr = "latest.exchange_id"
		selectKey = `latest.exchange_id AS group_key, e.name AS exchange_name,
		             e.logo_url AS exchange_logo, e.kind AS exchange_kind,
		             '' AS target_symbol`
	case tickers.GroupByTarget:
		groupExpr = "latest.target_symbol"
		selectKey = `latest.target_symbol AS group_key, '' AS exchange_name,
		             NULL::text AS exchange_logo, '' AS exchange_kind,
		             latest.target_symbol AS target_symbol`
	default:
		return tickers.Distribution{}, fmt.Errorf("unsupported group_by: %q", q.GroupBy)
	}

	// JOIN exchanges only needed for GroupBy=exchange (logo/name); for
	// GroupBy=target it's still in the inner CTE for the FROM clause, but
	// the outer SELECT doesn't read from it. Cheap because the lookup is tiny.
	join := "JOIN exchanges e ON e.id = latest.exchange_id"
	sql := `
WITH latest AS (
    SELECT DISTINCT ON (exchange_id, target_symbol)
           exchange_id, target_symbol, ts, volume_24h_base
      FROM token_tickers
     WHERE token_symbol = ? AND source_code = ?` + stalePredicate + `
     ORDER BY exchange_id, target_symbol, ts DESC
)
SELECT ` + selectKey + `,
       COALESCE(SUM(latest.volume_24h_base), 0)::numeric(38,18) AS volume_base
  FROM latest
  ` + join + `
 GROUP BY ` + groupExpr + `, exchange_name, exchange_logo, exchange_kind
 ORDER BY volume_base DESC, group_key ASC`

	type row struct {
		GroupKey     string          `gorm:"column:group_key"`
		ExchangeName string          `gorm:"column:exchange_name"`
		ExchangeLogo *string         `gorm:"column:exchange_logo"`
		ExchangeKind string          `gorm:"column:exchange_kind"`
		TargetSymbol string          `gorm:"column:target_symbol"`
		VolumeBase   decimal.Decimal `gorm:"column:volume_base"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return tickers.Distribution{}, fmt.Errorf("query distribution: %w", err)
	}

	total := decimal.Zero
	for _, r := range rows {
		total = total.Add(r.VolumeBase)
	}

	out := tickers.Distribution{
		Token:     q.Token,
		Source:    q.Source,
		Timestamp: time.Now().UTC(),
		GroupBy:   q.GroupBy,
		Total:     total,
		Rows:      make([]tickers.DistributionRow, 0, len(rows)),
	}
	if total.IsZero() {
		// No useful volume to bucket; return empty.
		return out, nil
	}
	hundred := decimal.NewFromInt(100)
	for _, r := range rows {
		if r.VolumeBase.IsZero() {
			continue
		}
		share := r.VolumeBase.DivRound(total, 8).Mul(hundred).Round(6)
		dr := tickers.DistributionRow{
			VolumeBase: r.VolumeBase,
			SharePct:   share,
		}
		if q.GroupBy == tickers.GroupByExchange {
			dr.Exchange = tickers.Exchange{
				ID:      r.GroupKey,
				Name:    r.ExchangeName,
				LogoURL: derefString(r.ExchangeLogo),
				Kind:    tickers.ExchangeKind(r.ExchangeKind),
			}
		} else {
			dr.TargetSymbol = r.TargetSymbol
		}
		out.Rows = append(out.Rows, dr)
	}
	return out, nil
}

// Compile-time: TickerRepository satisfies the application interface.
var _ apitickers.Repository = (*TickerRepository)(nil)

// --- helpers ---

func exchangesToEntities(in []tickers.Exchange) []entities.ExchangeEntity {
	out := make([]entities.ExchangeEntity, len(in))
	for i, e := range in {
		var logo *string
		if s := strings.TrimSpace(e.LogoURL); s != "" {
			ls := s
			logo = &ls
		}
		kind := string(e.Kind)
		if kind == "" {
			kind = string(tickers.ExchangeKindCEX)
		}
		seen := e.LastSeenAt
		if seen.IsZero() {
			seen = time.Now().UTC()
		}
		out[i] = entities.ExchangeEntity{
			ID:                  e.ID,
			Name:                e.Name,
			LogoURL:             logo,
			Kind:                kind,
			HasTradingIncentive: e.HasTradingIncentive,
			LastSeenAt:          seen.UTC(),
		}
	}
	return out
}

func tickersToEntities(in []tickers.Ticker) []entities.TokenTickerEntity {
	out := make([]entities.TokenTickerEntity, len(in))
	for i, t := range in {
		ent := entities.TokenTickerEntity{
			TokenSymbol:     string(t.Token),
			SourceCode:      string(t.Source),
			ExchangeID:      t.ExchangeID,
			TargetSymbol:    strings.ToLower(strings.TrimSpace(t.TargetSymbol)),
			Timestamp:       t.Timestamp.UTC(),
			LastPrice:       t.LastPrice,
			VolumeBase:      t.VolumeBase,
			BidAskSpreadPct: t.BidAskSpread,
			IsAnomaly:       t.IsAnomaly,
		}
		if s := strings.TrimSpace(t.TrustScore); s != "" {
			ts := s
			ent.TrustScore = &ts
		}
		if s := strings.TrimSpace(t.TradeURL); s != "" {
			ts := s
			ent.TradeURL = &ts
		}
		if !t.LastTradedAt.IsZero() {
			lt := t.LastTradedAt.UTC()
			ent.LastTradedAt = &lt
		}
		out[i] = ent
	}
	return out
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// percentChange returns ((now - past) / past * 100) rounded to 6dp. Returns
// nil when past is missing or zero (no meaningful percentage).
func percentChange(now decimal.Decimal, past *decimal.Decimal) *decimal.Decimal {
	if past == nil || past.IsZero() {
		return nil
	}
	delta := now.Sub(*past)
	pct := delta.DivRound(*past, 10).Mul(decimal.NewFromInt(100)).Round(6)
	return &pct
}
