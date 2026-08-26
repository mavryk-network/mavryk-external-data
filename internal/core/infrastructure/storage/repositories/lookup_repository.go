package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

	"gorm.io/gorm"
)

// LookupRepository serves the lookup tables (sources, tokens, rwa_pairs).
// Reads are unrestricted; writes are limited to the controlled mutations the
// RWA sync job needs (`UpsertRWAPair`, `DisableMissingRWAPairs`). Bulk loads
// of `tokens`/`sources` continue to be migration-driven.
type LookupRepository struct {
	db *gorm.DB
}

func NewLookupRepository(db *gorm.DB) *LookupRepository {
	return &LookupRepository{db: db}
}

// Tokens loads every row from `tokens`. Used at startup to populate the in-process
// token registry; never called on the hot path.
func (r *LookupRepository) Tokens(ctx context.Context) ([]prices.TokenInfo, error) {
	var rows []entities.TokenEntity
	if err := r.db.WithContext(ctx).Order("symbol").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load tokens: %w", err)
	}
	out := make([]prices.TokenInfo, 0, len(rows))
	for _, e := range rows {
		info := prices.TokenInfo{
			Symbol:   prices.Token(e.Symbol),
			Name:     e.Name,
			Decimals: int(e.Decimals),
			Enabled:  e.Enabled,
		}
		if e.CGID != nil {
			info.CoinGeckoID = *e.CGID
		}
		out = append(out, info)
	}
	return out, nil
}

// RWAPairs loads every row from `rwa_pairs`. Filtering is done in-memory by callers.
func (r *LookupRepository) RWAPairs(ctx context.Context) ([]prices.RWAPair, error) {
	var rows []entities.RWAPairEntity
	if err := r.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load rwa_pairs: %w", err)
	}
	out := make([]prices.RWAPair, 0, len(rows))
	for _, e := range rows {
		out = append(out, entityToRWAPair(e))
	}
	return out, nil
}

// EnabledRWAPairs returns enabled rows from `rwa_pairs` sorted by
// (source_code, base_symbol, quote_symbol). Backs the `/v1/pairs/rwa`
// discovery endpoint — stable order matters for clients that diff the
// catalog between polls.
func (r *LookupRepository) EnabledRWAPairs(ctx context.Context) ([]prices.RWAPair, error) {
	var rows []entities.RWAPairEntity
	err := r.db.WithContext(ctx).
		Where("enabled = ?", true).
		Order("source_code, base_symbol, quote_symbol").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load enabled rwa_pairs: %w", err)
	}
	out := make([]prices.RWAPair, 0, len(rows))
	for _, e := range rows {
		out = append(out, entityToRWAPair(e))
	}
	return out, nil
}

// LookupRWAPair returns one pair by ID, or ErrPairNotFound.
func (r *LookupRepository) LookupRWAPair(ctx context.Context, id int64) (prices.RWAPair, error) {
	var e entities.RWAPairEntity
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&e).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return prices.RWAPair{}, prices.ErrPairNotFound
		}
		return prices.RWAPair{}, fmt.Errorf("lookup rwa_pair %d: %w", id, err)
	}
	return entityToRWAPair(e), nil
}

// LookupRWAPairBySymbol returns the single enabled pair matching
// (base, quote) ignoring case. Returns:
//
//   - prices.ErrPairNotFound        — no enabled row matches.
//   - *prices.PairAmbiguousError    — 2+ enabled rows share the same
//     (base, quote); the IDs slice is populated for the caller to
//     surface to the operator.
//
// `base` and `quote` must already be lowercased by the caller.
func (r *LookupRepository) LookupRWAPairBySymbol(ctx context.Context, base, quote string) (prices.RWAPair, error) {
	var rows []entities.RWAPairEntity
	err := r.db.WithContext(ctx).
		Where("LOWER(base_symbol) = ?", base).
		Where("LOWER(quote_symbol) = ?", quote).
		Where("enabled = ?", true).
		Order("id").
		Limit(2). // LIMIT 2 = enough to detect a collision.
		Find(&rows).Error
	if err != nil {
		return prices.RWAPair{}, fmt.Errorf("lookup rwa_pair by symbol %s-%s: %w", base, quote, err)
	}
	switch len(rows) {
	case 0:
		return prices.RWAPair{}, prices.ErrPairNotFound
	case 1:
		return entityToRWAPair(rows[0]), nil
	default:
		return prices.RWAPair{}, &prices.PairAmbiguousError{
			Base:  base,
			Quote: quote,
			IDs:   []int64{rows[0].ID, rows[1].ID},
		}
	}
}

// RWAPairDisabledReasonSyncMissing marks rows the discovery sync disabled
// because the pair fell out of the upstream allowlist. Only rows with this
// reason are auto-re-enabled when the pair reappears; NULL/other reasons are
// operator decisions and survive every sync.
const RWAPairDisabledReasonSyncMissing = "sync_missing"

// UpsertRWAPair finds-or-creates by `(source_code, orderbook_addr)` and
// updates metadata + `last_synced_at`. Returns the persisted ID.
//
// Operator overrides survive sync: `enabled` is set on INSERT (to true) and
// re-set to true only for rows the sync itself disabled (disabled_reason =
// 'sync_missing'). Rows disabled by an operator keep their state; reset them
// by editing the row in DB.
func (r *LookupRepository) UpsertRWAPair(ctx context.Context, p prices.RWAPair, syncedAt time.Time) (int64, error) {
	if p.Source == "" || p.OrderbookAddr == "" {
		return 0, fmt.Errorf("upsert rwa_pair: source and orderbook_addr are required")
	}

	tokenAddr := stringPtrOrNil(p.TokenAddr)
	quoteAddr := stringPtrOrNil(p.QuoteAddr)
	orderbookAddr := stringPtrOrNil(p.OrderbookAddr)
	equiteezID := p.EquiteezOrderbookID

	tx := r.db.WithContext(ctx)

	var existing entities.RWAPairEntity
	res := tx.Where("source_code = ? AND orderbook_addr = ?", string(p.Source), p.OrderbookAddr).Take(&existing)
	switch {
	case errors.Is(res.Error, gorm.ErrRecordNotFound):
		row := entities.RWAPairEntity{
			BaseSymbol:          p.BaseSymbol,
			QuoteSymbol:         p.QuoteSymbol,
			SourceCode:          string(p.Source),
			TokenAddr:           tokenAddr,
			QuoteAddr:           quoteAddr,
			OrderbookAddr:       orderbookAddr,
			EquiteezOrderbookID: equiteezID,
			Enabled:             true,
			LastSyncedAt:        &syncedAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			return 0, fmt.Errorf("insert rwa_pair: %w", err)
		}
		return row.ID, nil
	case res.Error != nil:
		return 0, fmt.Errorf("lookup rwa_pair: %w", res.Error)
	default:
		// Update metadata only — leave `enabled` for the operator to control,
		// except rows the sync itself disabled: the pair is present upstream
		// again, so undo the sync's own soft-disable.
		// equiteez_orderbook_id and quote_addr are updated only when the caller
		// supplies a value; preserves a previously-good value if a degraded sync
		// response omitted it (currency rows are a nested, independently-nullable
		// part of the indexer payload).
		updates := map[string]any{
			"base_symbol":    p.BaseSymbol,
			"quote_symbol":   p.QuoteSymbol,
			"token_addr":     tokenAddr,
			"last_synced_at": syncedAt,
		}
		if !existing.Enabled && existing.DisabledReason != nil &&
			*existing.DisabledReason == RWAPairDisabledReasonSyncMissing {
			updates["enabled"] = true
			updates["disabled_reason"] = nil
		}
		if equiteezID != nil {
			updates["equiteez_orderbook_id"] = equiteezID
		}
		if quoteAddr != nil {
			updates["quote_addr"] = quoteAddr
		}
		err := tx.Model(&entities.RWAPairEntity{}).
			Where("id = ?", existing.ID).
			Updates(updates).Error
		if err != nil {
			return 0, fmt.Errorf("update rwa_pair: %w", err)
		}
		return existing.ID, nil
	}
}

// DisableMissingRWAPairs marks every pair for `source` whose ID is NOT in
// `keepIDs` as `enabled=false` with disabled_reason='sync_missing', so a later
// sync that sees the pair again can re-enable it (see UpsertRWAPair). Used by
// the sync to handle pairs that fell out of the upstream allowlist.
//
// Pairs already disabled stay disabled. Pairs in `keepIDs` are not touched
// (operator overrides are preserved).
func (r *LookupRepository) DisableMissingRWAPairs(ctx context.Context, source prices.Source, keepIDs []int64) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&entities.RWAPairEntity{}).
		Where("source_code = ? AND enabled = ?", string(source), true)
	if len(keepIDs) > 0 {
		tx = tx.Where("id NOT IN ?", keepIDs)
	}
	res := tx.Updates(map[string]any{
		"enabled":         false,
		"disabled_reason": RWAPairDisabledReasonSyncMissing,
	})
	if res.Error != nil {
		return 0, fmt.Errorf("disable missing rwa_pairs: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func entityToRWAPair(e entities.RWAPairEntity) prices.RWAPair {
	pair := prices.RWAPair{
		ID:                  e.ID,
		BaseSymbol:          e.BaseSymbol,
		QuoteSymbol:         e.QuoteSymbol,
		Source:              prices.Source(e.SourceCode),
		EquiteezOrderbookID: e.EquiteezOrderbookID,
		Enabled:             e.Enabled,
		LastSyncedAt:        e.LastSyncedAt,
	}
	if e.TokenAddr != nil {
		pair.TokenAddr = *e.TokenAddr
	}
	if e.QuoteAddr != nil {
		pair.QuoteAddr = *e.QuoteAddr
	}
	if e.OrderbookAddr != nil {
		pair.OrderbookAddr = *e.OrderbookAddr
	}
	return pair
}

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
