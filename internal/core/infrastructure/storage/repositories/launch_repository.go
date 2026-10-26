package repositories

import (
	"context"
	"fmt"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LaunchRepository owns `rwa_launches`: the primary-issuance state the sync job
// writes and GET /v1/rwa reads.
type LaunchRepository struct {
	db *gorm.DB
}

func NewLaunchRepository(db *gorm.DB) *LaunchRepository {
	return &LaunchRepository{db: db}
}

// Upsert writes one launch, creating the row on first sight. `enabled` follows
// the UpsertRWAPair contract: re-set on conflict only for rows the sync itself
// disabled; operator disables are never resurrected.
func (r *LaunchRepository) Upsert(ctx context.Context, l prices.RWALaunch, now time.Time) error {
	if l.Source == "" || l.TokenAddr == "" {
		return fmt.Errorf("launch source and token_addr are required")
	}
	e := entities.RWALaunchEntity{
		SourceCode:  string(l.Source),
		InAllowlist: l.InAllowlist, IndexerGeneration: l.IndexerGeneration, LaunchpadAddr: l.LaunchpadAddr,
		TokenAddr:           l.TokenAddr,
		TokenID:             l.TokenID,
		LaunchID:            l.LaunchID,
		Name:                l.Name,
		Status:              l.Status,
		Active:              l.Active,
		BaseSymbol:          l.BaseSymbol,
		QuoteSymbol:         l.QuoteSymbol,
		QuoteAddr:           stringPtrOrNil(l.QuoteAddr),
		PricingQuoteTokenID: l.PricingQuoteTokenID,
		Price:               decimal.NullDecimal{Decimal: l.Price, Valid: !l.Price.IsZero()},
		TotalBought:         decimal.NullDecimal{Decimal: l.TotalBought, Valid: true},
		MaxAmountCap:        decimal.NullDecimal{Decimal: l.MaxAmountCap, Valid: true},
		ProgressPercent:     l.ProgressPercent,
		SaleStart:           l.SaleStart,
		SaleEnd:             l.SaleEnd,
		SaleClosed:          l.SaleClosed,
		Enabled:             true, // insert-only; see DoUpdates below
		LastSyncedAt:        now.UTC(),
		UpdatedAt:           now.UTC(),
	}
	updateCols := []string{
		"token_id", "in_allowlist", "indexer_generation", "launchpad_addr",
		"launch_id",
		"name",
		"status",
		"active",
		"base_symbol",
		"quote_symbol",
		"pricing_quote_token_id",
		"price",
		"total_bought",
		"max_amount_cap",
		"progress_percent",
		"sale_start",
		"sale_end",
		"sale_closed",
		"last_synced_at",
		"updated_at",
		// NOTE: `enabled` intentionally omitted — operator-owned.
	}
	// quote_addr updates only when the sync produced a value — same preserve
	// contract as rwa_pairs.quote_addr: a payment row missing its token ref in
	// a degraded response must not wipe a previously-good address.
	if l.QuoteAddr != "" {
		updateCols = append(updateCols, "quote_addr")
	}
	set := clause.AssignmentColumns(updateCols)
	set = append(set,
		clause.Assignment{
			Column: clause.Column{Name: "enabled"},
			Value: gorm.Expr("CASE WHEN rwa_launches.disabled_reason = ? THEN TRUE ELSE rwa_launches.enabled END",
				RWAPairDisabledReasonSyncMissing),
		},
		clause.Assignment{
			Column: clause.Column{Name: "disabled_reason"},
			Value: gorm.Expr("CASE WHEN rwa_launches.disabled_reason = ? THEN NULL ELSE rwa_launches.disabled_reason END",
				RWAPairDisabledReasonSyncMissing),
		},
	)
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_code"}, {Name: "token_addr"}},
		DoUpdates: set,
	}).Create(&e)
	if res.Error != nil {
		return fmt.Errorf("upsert rwa_launches: %w", res.Error)
	}
	return nil
}

// EnabledLaunches returns every enabled launch for `source`, ordered by symbol
// so the API response is deterministic.
func (r *LaunchRepository) EnabledLaunches(ctx context.Context, source prices.Source) ([]prices.RWALaunch, error) {
	var rows []entities.RWALaunchEntity
	err := r.db.WithContext(ctx).
		Where("source_code = ? AND enabled AND in_allowlist", string(source)).
		Order("base_symbol ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list rwa_launches: %w", err)
	}
	out := make([]prices.RWALaunch, 0, len(rows))
	for _, e := range rows {
		out = append(out, entityToLaunch(e))
	}
	return out, nil
}

// LaunchBySymbol resolves a `{base}-{quote}` symbol to its enabled launch.
// Comparison is case-insensitive to match the URL parsing, which lowercases.
// found=false (not an error) when the symbol is not a primary-market asset, so
// the caller can keep its own 404. Two enabled launches sharing a symbol
// return PairAmbiguousError (409) — same contract as LookupRWAPairBySymbol —
// instead of Take()'s planner-dependent arbitrary row.
func (r *LaunchRepository) LaunchBySymbol(ctx context.Context, source prices.Source, base, quote string) (prices.RWALaunch, bool, error) {
	var rows []entities.RWALaunchEntity
	err := r.db.WithContext(ctx).
		Where("source_code = ? AND enabled AND in_allowlist AND lower(base_symbol) = lower(?) AND lower(quote_symbol) = lower(?)",
			string(source), base, quote).
		Order("token_addr").
		Limit(2).
		Find(&rows).Error
	if err != nil {
		return prices.RWALaunch{}, false, fmt.Errorf("lookup rwa_launch by symbol: %w", err)
	}
	switch len(rows) {
	case 0:
		return prices.RWALaunch{}, false, nil
	case 1:
		return entityToLaunch(rows[0]), true, nil
	default:
		return prices.RWALaunch{}, false, &prices.PairAmbiguousError{
			Base:  base,
			Quote: quote,
			IDs:   []int64{int64(rows[0].LaunchID), int64(rows[1].LaunchID)},
		}
	}
}

func entityToLaunch(e entities.RWALaunchEntity) prices.RWALaunch {
	quoteAddr := ""
	if e.QuoteAddr != nil {
		quoteAddr = *e.QuoteAddr
	}
	return prices.RWALaunch{
		Source:      prices.Source(e.SourceCode),
		InAllowlist: e.InAllowlist, IndexerGeneration: e.IndexerGeneration, LaunchpadAddr: e.LaunchpadAddr,
		TokenAddr:           e.TokenAddr,
		TokenID:             e.TokenID,
		LaunchID:            e.LaunchID,
		Name:                e.Name,
		Status:              e.Status,
		Active:              e.Active,
		BaseSymbol:          e.BaseSymbol,
		QuoteSymbol:         e.QuoteSymbol,
		QuoteAddr:           quoteAddr,
		PricingQuoteTokenID: e.PricingQuoteTokenID,
		Price:               e.Price.Decimal,
		TotalBought:         e.TotalBought.Decimal,
		MaxAmountCap:        e.MaxAmountCap.Decimal,
		ProgressPercent:     e.ProgressPercent,
		SaleStart:           e.SaleStart,
		SaleEnd:             e.SaleEnd,
		SaleClosed:          e.SaleClosed,
		LastSyncedAt:        e.LastSyncedAt,
		Enabled:             e.Enabled,
	}
}
