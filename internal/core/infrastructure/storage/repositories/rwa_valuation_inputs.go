package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/valuation"
	"time"
)

func (r *RWAValuationRepository) MarketAt(ctx context.Context, pairID int64, at time.Time) (prices.RWAMarketSnapshot, bool, error) {
	var p prices.RWAMarketSnapshot
	err := r.db.WithContext(ctx).Table("rwa_market_observations").Where("pair_id=? AND observed_at<=?", pairID, at).Order("observed_at DESC").Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, false, nil
	}
	return p, err == nil, err
}

func (r *RWAValuationRepository) Delisted(ctx context.Context, network string, allowed []string) ([]string, error) {
	var out []string
	q := r.db.WithContext(ctx).Table("rwa_valuation_current").Where("network=? AND snapshot IS NOT NULL AND COALESCE(snapshot->>'reason','')<>'asset_delisted'", network)
	if len(allowed) > 0 {
		q = q.Where("contract NOT IN ?", allowed)
	}
	err := q.Distinct("contract").Pluck("contract", &out).Error
	return out, err
}

func (r *RWAValuationRepository) ForContract(ctx context.Context, network, contract string) ([]valuation.Snapshot, error) {
	var rows []struct{ Snapshot []byte }
	err := r.db.WithContext(ctx).Table("rwa_valuation_current").Select("snapshot").Where("network=? AND contract=? AND snapshot IS NOT NULL", network, contract).Find(&rows).Error
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

// RoutingAt uses recorded valuation provenance, never today's pair directory
// to assert that a secondary market existed at an earlier instant.
func (r *RWAValuationRepository) RoutingAt(ctx context.Context, id valuation.SupplyIdentity, at time.Time, maxAge time.Duration) ([]valuation.Point, error) {
	var rows []struct{ Snapshot []byte }
	err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON(symbol,quote_contract,quote_token_id) snapshot FROM rwa_valuation_points
 WHERE network=? AND contract=? AND token_id=? AND as_of<=? AND as_of>=? AND NOT invalidated ORDER BY symbol,quote_contract,quote_token_id,as_of DESC`, id.Network, id.Contract, id.TokenID, at, at.Add(-maxAge)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := []valuation.Point{}
	for _, row := range rows {
		var p valuation.Point
		if err := json.Unmarshal(row.Snapshot, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *RWAValuationRepository) RecordAttempt(ctx context.Context, network, contract, reason string, at time.Time) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO rwa_valuation_attempts(network,contract,reason,attempted_at) VALUES(?,?,?,?)
 ON CONFLICT(network,contract) DO UPDATE SET reason=excluded.reason,attempted_at=excluded.attempted_at WHERE excluded.attempted_at>=rwa_valuation_attempts.attempted_at`, network, contract, reason, at).Error
}

type ValuationAttempt struct {
	Contract    string    `json:"contract"`
	Reason      string    `json:"reason"`
	AttemptedAt time.Time `json:"attempted_at"`
}

func (r *RWAValuationRepository) Attempts(ctx context.Context, network string) ([]ValuationAttempt, error) {
	var out []ValuationAttempt
	err := r.db.WithContext(ctx).Table("rwa_valuation_attempts").Where("network=? AND reason<>''", network).Order("contract").Limit(100).Find(&out).Error
	return out, err
}
