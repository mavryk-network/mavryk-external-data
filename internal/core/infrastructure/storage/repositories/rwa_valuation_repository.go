package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/valuation"
)

var ErrValuationSuperseded = errors.New("valuation revision superseded")

type RWAValuationRepository struct{ db *gorm.DB }

func NewRWAValuationRepository(db *gorm.DB) *RWAValuationRepository {
	return &RWAValuationRepository{db: db}
}

type valuationCurrent struct {
	Revision    int64
	Fingerprint string
	Snapshot    []byte
	AsOf        *time.Time
}

func (r *RWAValuationRepository) Current(ctx context.Context, id valuation.SupplyIdentity) (valuation.Snapshot, bool, error) {
	var row valuationCurrent
	result := r.db.WithContext(ctx).Table("rwa_valuation_current").Where("network=? AND contract=? AND token_id=?", id.Network, id.Contract, id.TokenID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) || result.Error == nil && len(row.Snapshot) == 0 {
		return valuation.Snapshot{}, false, nil
	}
	if result.Error != nil {
		return valuation.Snapshot{}, false, result.Error
	}
	var s valuation.Snapshot
	err := json.Unmarshal(row.Snapshot, &s)
	return s, err == nil, err
}

// Save atomically archives history and publishes a monotonically revised current
// snapshot. The expected revision belongs to the inputs, not the HTTP request.
func (r *RWAValuationRepository) Save(ctx context.Context, s valuation.Snapshot, expected int64, historical bool) (valuation.Snapshot, error) {
	if !s.Identity.Valid() || s.AsOf.IsZero() {
		return s, fmt.Errorf("invalid valuation identity/time")
	}
	s.SchemaVersion = 1
	s.PolicyVersion = valuation.PolicyVersion
	s.Supply.PolicyVersion = valuation.PolicyVersion
	s.Revision = ""
	fingerprint := valuation.Fingerprint(s)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO rwa_valuation_current(network,contract,token_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID).Error; err != nil {
			return err
		}
		var old valuationCurrent
		if err := tx.Raw(`SELECT revision,fingerprint,snapshot,as_of FROM rwa_valuation_current WHERE network=? AND contract=? AND token_id=? FOR UPDATE`, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID).Scan(&old).Error; err != nil {
			return err
		}
		if old.Fingerprint == fingerprint {
			return json.Unmarshal(old.Snapshot, &s)
		}
		publish := !historical || old.AsOf == nil || !s.AsOf.Before(*old.AsOf)
		// Reconstruction cannot replace direct evidence at the same instant,
		// including the current projection. Corrections use the normal CAS path.
		if historical && old.AsOf != nil && s.AsOf.Equal(*old.AsOf) {
			var previous valuation.Snapshot
			if err := json.Unmarshal(old.Snapshot, &previous); err != nil {
				return err
			}
			if previous.Supply.Provenance == "observed" {
				publish = false
			}
		}
		if publish && (old.Revision != expected || !s.Invalidated && old.AsOf != nil && s.AsOf.Before(*old.AsOf)) {
			return ErrValuationSuperseded
		}
		if s.Invalidated && s.Reason == "chain_reorg" {
			// An explicit orphan invalidation affects all recorded observations
			// at/above this block; their aggregate pointers remain, now as gaps.
			if err := tx.Exec(`UPDATE rwa_valuation_points SET invalidated=TRUE WHERE network=? AND contract=? AND token_id=? AND block_level>=?`, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID, s.Supply.Block.Level).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE rwa_supply_observations SET invalidated=TRUE WHERE network=? AND contract=? AND token_id=? AND block_level>=?`, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID, s.Supply.Block.Level).Error; err != nil {
				return err
			}
		}
		if !s.CurrentOnly {
			obsID := valuation.Fingerprint(struct {
				Identity valuation.SupplyIdentity
				At       time.Time
			}{s.Identity, s.Supply.ObservedAt})
			supplyJSON, err := json.Marshal(s.Supply)
			if err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO rwa_supply_observations(observed_at,observation_id,network,contract,token_id,block_level,block_hash,scope_version,total_supply,excluded_supply,circulating_supply,snapshot,invalidated)
 VALUES(?,?,?,?,?,?,?,?,NULLIF(?,'')::numeric,NULLIF(?,'')::numeric,NULLIF(?,'')::numeric,?::jsonb,?)
 ON CONFLICT(observed_at,observation_id) DO UPDATE SET snapshot=excluded.snapshot,block_level=excluded.block_level,block_hash=excluded.block_hash,scope_version=excluded.scope_version,total_supply=excluded.total_supply,excluded_supply=excluded.excluded_supply,circulating_supply=excluded.circulating_supply,invalidated=excluded.invalidated
 WHERE rwa_supply_observations.snapshot->>'provenance'!='observed' OR excluded.snapshot->>'provenance'='observed'`, s.Supply.ObservedAt, obsID, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID, s.Supply.Block.Level, s.Supply.Block.Hash, s.Supply.ScopeVersion, s.Supply.Total, s.Supply.Excluded, s.Supply.Circulating, string(supplyJSON), s.Invalidated).Error; err != nil {
				return err
			}
			for _, p := range s.Valuations {
				pid := valuation.Fingerprint(struct {
					Identity valuation.SupplyIdentity
					Symbol   string
					Quote    valuation.Quote
					At       time.Time
				}{s.Identity, p.Symbol, p.Quote, p.AsOf})
				body, err := json.Marshal(p)
				if err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO rwa_valuation_points(as_of,point_id,network,contract,token_id,symbol,quote_contract,quote_token_id,observation_id,block_level,block_hash,market_cap,fdv,snapshot,invalidated)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?::jsonb,?) ON CONFLICT(as_of,point_id) DO UPDATE SET observation_id=excluded.observation_id,block_level=excluded.block_level,block_hash=excluded.block_hash,market_cap=excluded.market_cap,fdv=excluded.fdv,snapshot=excluded.snapshot,invalidated=excluded.invalidated
 WHERE NOT ? OR NOT EXISTS(SELECT 1 FROM rwa_supply_observations o WHERE o.observation_id=rwa_valuation_points.observation_id AND o.snapshot->>'provenance'='observed')`, p.AsOf, pid, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID, p.Symbol, p.Quote.Contract, p.Quote.TokenID, obsID, s.Supply.Block.Level, s.Supply.Block.Hash, p.MarketCap, p.FDV, string(body), s.Invalidated, historical).Error; err != nil {
					return err
				}
			}
		}
		if !publish {
			return nil
		}
		if old.Revision == int64(9223372036854775807) {
			return fmt.Errorf("valuation revision exhausted")
		}
		s.Revision = strconv.FormatInt(old.Revision+1, 10)
		body, err := json.Marshal(s)
		if err != nil {
			return err
		}
		return tx.Exec(`UPDATE rwa_valuation_current SET revision=?,fingerprint=?,snapshot=?::jsonb,as_of=? WHERE network=? AND contract=? AND token_id=?`, old.Revision+1, fingerprint, string(body), s.AsOf, s.Identity.Network, s.Identity.Contract, s.Identity.TokenID).Error
	})
	return s, err
}

func (r *RWAValuationRepository) List(ctx context.Context, network, after string, limit int) ([]valuation.Snapshot, error) {
	var rows []struct{ Snapshot []byte }
	err := r.db.WithContext(ctx).Raw(`SELECT snapshot FROM rwa_valuation_current WHERE network=? AND snapshot IS NOT NULL AND (contract||':'||token_id)>? ORDER BY contract,token_id LIMIT ?`, network, after, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]valuation.Snapshot, 0, len(rows))
	for _, row := range rows {
		var s valuation.Snapshot
		if err := json.Unmarshal(row.Snapshot, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *RWAValuationRepository) BySymbol(ctx context.Context, network, symbol string) ([]valuation.Snapshot, error) {
	var rows []struct{ Snapshot []byte }
	err := r.db.WithContext(ctx).Raw(`SELECT snapshot FROM rwa_valuation_current WHERE network=? AND EXISTS(SELECT 1 FROM jsonb_array_elements(snapshot->'valuations') p WHERE p->>'symbol'=?) LIMIT 2`, network, symbol).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := []valuation.Snapshot{}
	for _, row := range rows {
		var s valuation.Snapshot
		if err := json.Unmarshal(row.Snapshot, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

type ValuationBucket struct {
	T           time.Time
	Point       valuation.Point
	Invalidated bool
}
type ValuationCoverage struct {
	AvailableFrom *time.Time `json:"available_from"`
	Through       *time.Time `json:"through"`
}

func (r *RWAValuationRepository) Series(ctx context.Context, id valuation.SupplyIdentity, symbol string, iv app.Interval, from, to time.Time, limit int) ([]ValuationBucket, ValuationCoverage, error) {
	source := "rwa_valuation_1m"
	width := "1 minute"
	switch iv {
	case app.Interval1m:
	case app.Interval5m:
		width = "5 minutes"
	case app.Interval15m:
		width = "15 minutes"
	case app.Interval1h:
		source = "rwa_valuation_1h"
		width = "1 hour"
	case app.Interval4h:
		source = "rwa_valuation_1h"
		width = "4 hours"
	case app.Interval1d:
		source = "rwa_valuation_1d"
		width = "1 day"
	default:
		return nil, ValuationCoverage{}, fmt.Errorf("unsupported interval")
	}
	var coverage ValuationCoverage
	if err := r.db.WithContext(ctx).Raw(`SELECT MIN(as_of) available_from,MAX(as_of) through FROM rwa_valuation_points WHERE network=? AND contract=? AND token_id=? AND symbol=?`, id.Network, id.Contract, id.TokenID, symbol).Scan(&coverage).Error; err != nil {
		return nil, coverage, err
	}
	if coverage.AvailableFrom != nil {
		v := coverage.AvailableFrom.UTC()
		coverage.AvailableFrom = &v
	}
	if coverage.Through != nil {
		v := coverage.Through.UTC()
		coverage.Through = &v
	}
	var rows []struct {
		T           time.Time
		Snapshot    []byte
		Invalidated bool
	}
	// Source and width come only from the closed interval switch above.
	query := fmt.Sprintf(`WITH buckets AS (SELECT time_bucket(?::interval,bucket) t,last(point_id,as_of) pid,max(as_of) a FROM %s WHERE network=? AND contract=? AND token_id=? AND symbol=? AND bucket>=? AND bucket<? GROUP BY 1 ORDER BY 1 LIMIT ?)
 SELECT b.t,p.snapshot,p.invalidated FROM buckets b JOIN rwa_valuation_points p ON p.point_id=b.pid AND p.as_of=b.a ORDER BY b.t`, source)
	duration, _ := app.BucketDuration(iv)
	err := r.db.WithContext(ctx).Raw(query, width, id.Network, id.Contract, id.TokenID, symbol, from.UTC().Truncate(duration), to, limit).Scan(&rows).Error
	if err != nil {
		return nil, coverage, err
	}
	out := make([]ValuationBucket, 0, len(rows))
	for _, row := range rows {
		var p valuation.Point
		if err := json.Unmarshal(row.Snapshot, &p); err != nil {
			return nil, coverage, err
		}
		// Only boundary buckets may require a different close. One indexed
		// at-or-before lookup avoids leaking observations after the requested to.
		if p.AsOf.Before(from) || !p.AsOf.Before(to) {
			start, end := row.T, row.T.Add(duration)
			if start.Before(from) {
				start = from
			}
			if end.After(to) {
				end = to
			}
			var boundary struct {
				Snapshot    []byte
				Invalidated bool
			}
			res := r.db.WithContext(ctx).Raw(`SELECT snapshot,invalidated FROM rwa_valuation_points WHERE network=? AND contract=? AND token_id=? AND symbol=? AND as_of>=? AND as_of<? ORDER BY as_of DESC LIMIT 1`, id.Network, id.Contract, id.TokenID, symbol, start, end).Scan(&boundary)
			if res.Error != nil {
				return nil, coverage, res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			if err := json.Unmarshal(boundary.Snapshot, &p); err != nil {
				return nil, coverage, err
			}
			row.Invalidated = boundary.Invalidated
		}
		out = append(out, ValuationBucket{T: row.T, Point: p, Invalidated: row.Invalidated})
	}
	return out, coverage, nil
}

func (r *RWAValuationRepository) Refresh(ctx context.Context, from, to time.Time) error {
	for _, view := range []string{"rwa_valuation_1m", "rwa_valuation_1h", "rwa_valuation_1d"} {
		if err := r.db.WithContext(ctx).Exec("CALL refresh_continuous_aggregate(?::regclass,?::timestamptz,?::timestamptz)", view, from, to).Error; err != nil {
			return err
		}
	}
	return nil
}
