package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"

	"gorm.io/gorm"
)

var ErrAllowlistUnavailable = errors.New("allowlist_unavailable")

// WithEquiteezLock serializes catalog/live/history workers across replicas.
// The transaction owns only the advisory lock; writes use their own short
// transactions, and failed ticks can safely retry after the lock is released.
func (r *LookupRepository) WithEquiteezLock(ctx context.Context, run func() error) error {
	return r.withAdvisoryLock(ctx, 478123901, run)
}

func (r *LookupRepository) WithAllowlistLock(ctx context.Context, run func() error) error {
	return r.withAdvisoryLock(ctx, 478123902, run)
}

func (r *LookupRepository) WithSupplyLock(ctx context.Context, run func() error) error {
	return r.withAdvisoryLock(ctx, 478123903, run)
}

// Backfills serialize with each other while live supply/pricing keep running.
func (r *LookupRepository) WithValuationBackfillLock(ctx context.Context, run func() error) error {
	return r.withAdvisoryLock(ctx, 478123904, run)
}

func (r *LookupRepository) withAdvisoryLock(ctx context.Context, key int64, run func() error) error {
	for {
		var acquired bool
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", key).Scan(&acquired).Error; err != nil {
				return err
			}
			if !acquired {
				return nil
			}
			return run()
		})
		if err != nil || acquired {
			return err
		}
		// Release the unsuccessful transaction before waiting: holding a pool
		// connection here can deadlock the active worker with a small pool.
		// Retry in this tick so aligned schedules cannot starve catalog sync.
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *LookupRepository) CachedAllowlist(ctx context.Context, url string) (equiteez.ContractAllowlist, error) {
	var row struct{ Payload string }
	result := r.db.WithContext(ctx).Raw("SELECT payload::text AS payload FROM equiteez_allowlist WHERE id AND url = ?", url).Scan(&row)
	if result.Error != nil {
		return equiteez.ContractAllowlist{}, result.Error
	}
	if result.RowsAffected == 0 {
		return equiteez.ContractAllowlist{}, fmt.Errorf("no validated CRM allowlist cached for this URL")
	}
	return equiteez.ParseAllowlist([]byte(row.Payload))
}

// ApplyAllowlist changes listing membership only, even while the indexer is down.
// Legacy rows retain membership for relationships not yet discovered, unless
// CRM explicitly empties that relationship list. Known removals always delist.
func (r *LookupRepository) ApplyAllowlist(ctx context.Context, url string, a equiteez.ContractAllowlist, fresh bool) error {
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if fresh {
			if err := tx.Exec(`INSERT INTO rwa_allowlist_versions(url,version,payload,first_seen,last_seen)
 SELECT ?,?,?::jsonb,NOW(),NOW() WHERE NOT EXISTS (
 SELECT 1 FROM equiteez_allowlist WHERE id AND url=? AND payload=?::jsonb) ON CONFLICT DO NOTHING`, url, valuation.Fingerprint(a), string(payload), url, string(payload)).Error; err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO equiteez_allowlist(id,url,payload,fetched_at) VALUES(TRUE,?,?::jsonb,NOW())
    ON CONFLICT(id) DO UPDATE SET url=excluded.url,payload=excluded.payload,fetched_at=excluded.fetched_at,last_error=''`, url, string(payload)).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec(`WITH a AS (SELECT ?::jsonb AS body)
   UPDATE rwa_pairs SET in_allowlist = (
    COALESCE(token_addr IN (SELECT jsonb_array_elements_text(body->'base_tokens') FROM a),FALSE) AND
    COALESCE(quote_addr IN (SELECT jsonb_array_elements_text(body->'quote_tokens') FROM a),in_allowlist) AND
    EXISTS (SELECT 1 FROM a WHERE jsonb_array_length(body->'quote_tokens') > 0) AND
    COALESCE(orderbook_addr IN (SELECT jsonb_array_elements_text(body->'orderbooks') FROM a),FALSE))
   WHERE source_code='equiteez'`, string(payload)).Error; err != nil {
			return err
		}
		return tx.Exec(`WITH a AS (SELECT ?::jsonb AS body)
   UPDATE rwa_launches SET in_allowlist = (
    token_addr IN (SELECT jsonb_array_elements_text(body->'base_tokens') FROM a) AND
    COALESCE(quote_addr IN (SELECT jsonb_array_elements_text(body->'quote_tokens') FROM a),in_allowlist) AND
    EXISTS (SELECT 1 FROM a WHERE jsonb_array_length(body->'quote_tokens') > 0) AND
    EXISTS (SELECT 1 FROM a WHERE jsonb_array_length(body->'launchpads') > 0) AND
    CASE WHEN launchpad_addr='' THEN in_allowlist ELSE launchpad_addr IN (SELECT jsonb_array_elements_text(body->'launchpads') FROM a) END)
   WHERE source_code='equiteez'`, string(payload)).Error
	})
}

// AllowlistSnapshot is the common reader for catalog, primary and supply. The
// union of previously approved launchpads prevents removals inflating supply.
func (r *LookupRepository) AllowlistSnapshot(ctx context.Context, url string, at *time.Time) (equiteez.AllowlistSnapshot, error) {
	var row struct {
		Payload              string
		LastError            string
		FetchedAt, ValidFrom time.Time
	}
	q := r.db.WithContext(ctx)
	if at == nil {
		q = q.Raw(`SELECT a.payload::text payload,a.fetched_at,a.last_error,v.first_seen valid_from FROM equiteez_allowlist a
 JOIN rwa_allowlist_versions v ON v.url=a.url AND v.payload=a.payload WHERE a.id AND a.url=? ORDER BY v.first_seen DESC LIMIT 1`, url)
	} else {
		q = q.Raw(`SELECT payload::text payload,first_seen fetched_at,first_seen valid_from FROM rwa_allowlist_versions
 WHERE url=? AND first_seen<=? ORDER BY first_seen DESC LIMIT 1`, url, *at)
	}
	result := q.Scan(&row)
	if result.Error != nil {
		return equiteez.AllowlistSnapshot{}, result.Error
	}
	if result.RowsAffected == 0 {
		return equiteez.AllowlistSnapshot{}, ErrAllowlistUnavailable
	}
	doc, err := equiteez.ParseAllowlist([]byte(row.Payload))
	if err != nil {
		return equiteez.AllowlistSnapshot{}, err
	}
	row.FetchedAt, row.ValidFrom = row.FetchedAt.UTC(), row.ValidFrom.UTC()
	addresses := []string{}
	query := r.db.WithContext(ctx).Table("rwa_allowlist_versions").Where("url=?", url)
	query = query.Where("first_seen<=?", row.ValidFrom)
	if err := query.Select("DISTINCT jsonb_array_elements_text(payload->'launchpads') AS address").Order("address").Pluck("address", &addresses).Error; err != nil {
		return equiteez.AllowlistSnapshot{}, err
	}
	// A -> B -> A is a new scope even when A's current payload is identical:
	// treasury evidence may include launchpads first approved during B.
	version := valuation.Fingerprint(struct {
		Document   equiteez.ContractAllowlist
		Launchpads []string
		From       time.Time
	}{doc, addresses, row.ValidFrom})
	status := "verified"
	if row.LastError != "" {
		status = row.LastError
	}
	return equiteez.AllowlistSnapshot{Document: doc, Version: version, FetchedAt: row.FetchedAt, ValidFrom: row.ValidFrom, SourceStatus: status, TreasuryLaunchpads: addresses}, nil
}

func (r *LookupRepository) RecordAllowlistFailure(ctx context.Context, url string) error {
	return r.db.WithContext(ctx).Exec(`UPDATE equiteez_allowlist SET last_error='crm_unavailable' WHERE id AND url=?`, url).Error
}

// SaveEquiteezCatalog commits a verified snapshot atomically. Missing entries
// are intentionally untouched; only CRM can change their membership.
func (r *LookupRepository) SaveEquiteezCatalog(ctx context.Context, pairs []prices.RWAPair, launches []prices.RWALaunch, cfg config.EquiteezConfig, generation string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lookup := NewLookupRepository(tx)
		launchRepo := NewLaunchRepository(tx)
		now := time.Now().UTC()
		for _, pair := range pairs {
			if _, err := lookup.UpsertRWAPair(ctx, pair, now); err != nil {
				return err
			}
		}
		for _, launch := range launches {
			if err := launchRepo.Upsert(ctx, launch, now); err != nil {
				return err
			}
		}
		for _, address := range cfg.RecoverOrderbooks {
			if err := recoverContract(tx, "orderbook", address, generation); err != nil {
				return err
			}
		}
		for _, address := range cfg.RecoverLaunchTokens {
			if err := recoverContract(tx, "launch", address, generation); err != nil {
				return err
			}
		}
		return nil
	})
}

func recoverContract(tx *gorm.DB, kind, address, generation string) error {
	table, column := "rwa_pairs", "orderbook_addr"
	if kind == "launch" {
		table, column = "rwa_launches", "token_addr"
	}
	// Table/column are fixed literals above, never configuration input.
	query := fmt.Sprintf(`WITH recovered AS (
  INSERT INTO equiteez_recovery_applied(kind,address)
  SELECT ?, ? WHERE EXISTS (SELECT 1 FROM %s WHERE source_code='equiteez' AND %s=? AND in_allowlist AND indexer_generation=?)
  ON CONFLICT(kind,address) DO NOTHING RETURNING address)
  UPDATE %s SET enabled=TRUE WHERE source_code='equiteez' AND %s IN (SELECT address FROM recovered)`, table, column, table, column)
	return tx.Exec(query, kind, address, address, generation).Error
}

// CatalogGeneration refuses live/backfill writes before discovery has refreshed
// the contract mapping for this generation. Reads of saved history stay available.
func (r *LookupRepository) CatalogGeneration(ctx context.Context, id int64, generation string) error {
	p, err := r.LookupRWAPair(ctx, id)
	if err != nil {
		return err
	}
	if !p.Enabled || !p.InAllowlist || p.IndexerGeneration != generation {
		return errors.New("pair is disabled, delisted or awaiting discovery for this indexer generation")
	}
	return nil
}
