package repositories

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"quotes/internal/core/domain/valuation"
	"time"
)

type ValuationBackfill struct {
	JobID                 string `json:"job_id"`
	Network               string `json:"network"`
	Contract              string `json:"contract"`
	TokenID               string `json:"token_id"`
	FromLevel             int64  `json:"from_level"`
	NextLevel             int64  `json:"next_level"`
	ThroughLevel          int64  `json:"through_level"`
	Step                  int64  `json:"step"`
	RefreshedThroughLevel int64  `json:"refreshed_through_level"`
	Complete              bool   `json:"complete"`
}

func (r *RWAValuationRepository) BeginBackfill(ctx context.Context, job string, id valuation.SupplyIdentity, from, to, step int64) (ValuationBackfill, error) {
	var state ValuationBackfill
	if job == "" || len(job) > 128 || !id.Valid() || from <= 0 || to < from || step <= 0 || to > 1<<53 || step > 1<<53 {
		return state, fmt.Errorf("invalid backfill identity/range")
	}
	err := r.db.WithContext(ctx).Exec(`INSERT INTO rwa_valuation_backfill(job_id,network,contract,token_id,from_level,next_level,through_level,step) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, job, id.Network, id.Contract, id.TokenID, from, from, to, step).Error
	if err != nil {
		return state, err
	}
	err = r.db.WithContext(ctx).Table("rwa_valuation_backfill").Where("job_id=?", job).Take(&state).Error
	if err != nil {
		return state, err
	}
	if state.Network != id.Network || state.Contract != id.Contract || state.TokenID != id.TokenID || state.FromLevel != from || state.ThroughLevel != to || state.Step != step {
		return state, fmt.Errorf("backfill job already belongs to different inputs")
	}
	return state, nil
}

// History writes precede checkpoint advancement. A crash only repeats an
// idempotent observation; the checkpoint can never skip an unpublished write.
func (r *RWAValuationRepository) AdvanceBackfill(ctx context.Context, s ValuationBackfill, at *time.Time, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		next := s.NextLevel + s.Step
		result := tx.Exec(`UPDATE rwa_valuation_backfill SET next_level=?,complete=?,updated_at=NOW() WHERE job_id=? AND next_level=? AND NOT complete`, next, next > s.ThroughLevel, s.JobID, s.NextLevel)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrValuationSuperseded
		}
		return tx.Exec(`INSERT INTO rwa_valuation_backfill_results(job_id,level,as_of,reason) VALUES(?,?,?,?) ON CONFLICT(job_id,level) DO UPDATE SET as_of=excluded.as_of,reason=excluded.reason`, s.JobID, s.NextLevel, at, reason).Error
	})
}

func (r *RWAValuationRepository) BackfillCoverage(ctx context.Context, job string) (map[string]int64, error) {
	var rows []struct {
		Reason string
		Count  int64
	}
	err := r.db.WithContext(ctx).Raw(`SELECT reason,count(*) FROM rwa_valuation_backfill_results WHERE job_id=? GROUP BY reason`, job).Scan(&rows).Error
	out := map[string]int64{}
	for _, row := range rows {
		key := row.Reason
		if key == "" {
			key = "recorded"
		}
		out[key] = row.Count
	}
	return out, err
}

func (r *RWAValuationRepository) MarkBackfillRefreshed(ctx context.Context, job string, through int64) error {
	return r.db.WithContext(ctx).Exec(`UPDATE rwa_valuation_backfill SET refreshed_through_level=GREATEST(refreshed_through_level,?),updated_at=NOW() WHERE job_id=? AND ?>=from_level AND ?<next_level`, through, job, through, through).Error
}
