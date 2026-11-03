package jobs

import (
	"context"
	"errors"
	"fmt"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/mvkt"
	"quotes/internal/core/infrastructure/storage/repositories"
	"time"
)

type ValuationBackfillRequest struct {
	JobID                    string
	Identity                 valuation.SupplyIdentity
	FromLevel, ToLevel, Step int64
	MaxSteps                 int
}
type ValuationBackfillReport struct {
	State    repositories.ValuationBackfill `json:"checkpoint"`
	Coverage map[string]int64               `json:"coverage"`
}

// Backfill is an operator-triggered, bounded run. It never fetches today's CRM
// list as evidence for the past. Missing historical inputs remain gaps.
func (j *RWASupplyJob) Backfill(ctx context.Context, req ValuationBackfillRequest) (ValuationBackfillReport, error) {
	var report ValuationBackfillReport
	err := j.Lookup.WithValuationBackfillLock(ctx, func() error {
		var e error
		report, e = j.backfill(ctx, req)
		return e
	})
	return report, err
}

func (j *RWASupplyJob) backfill(ctx context.Context, req ValuationBackfillRequest) (ValuationBackfillReport, error) {
	var report ValuationBackfillReport
	if req.Identity.Network != j.Config.RWA.PricingNetwork() || req.MaxSteps < 1 || req.MaxSteps > 1000 {
		return report, fmt.Errorf("invalid backfill network/limit")
	}
	cfg := j.Config.RWASupply.WithDefaults(req.Identity.Network)
	state, err := j.Store.BeginBackfill(ctx, req.JobID, req.Identity, req.FromLevel, req.ToLevel, req.Step)
	if err != nil {
		return report, err
	}
	for n := 0; n < req.MaxSteps && !state.Complete; n++ {
		if err = ctx.Err(); err != nil {
			break
		}
		attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TokenTimeoutSeconds)*time.Second)
		block, readErr := j.Chain.GetBlockIdentity(attemptCtx, state.NextLevel)
		if readErr != nil {
			cancel()
			err = readErr
			break
		} // transport failure keeps checkpoint for retry
		at := block.Timestamp
		if at.IsZero() {
			cancel()
			err = mvkt.SupplyError("invalid_timestamp")
			break
		}
		scope, scopeErr := j.Scope.At(attemptCtx, at)
		reason := ""
		if scopeErr != nil {
			// Do not classify database outages as permanent historical gaps.
			if !errors.Is(scopeErr, repositories.ErrAllowlistUnavailable) {
				cancel()
				err = scopeErr
				break
			}
			reason = "allowlist_unavailable"
		} else {
			allowed := false
			for _, address := range scope.Document.BaseTokens {
				if address == req.Identity.Contract {
					allowed = true
					break
				}
			}
			if !allowed {
				reason = "asset_not_listed"
			} else {
				token, e := j.Chain.ResolveSingleFA2Token(attemptCtx, req.Identity.Contract)
				switch {
				case e != nil:
					readErr = e
				case token.TokenID != req.Identity.TokenID:
					readErr = mvkt.SupplyError("identity_mismatch")
				default:
					chain, e := mvkt.HistoricalSupplyContext(attemptCtx, j.Chain, cfg.ExpectedChainID, state.NextLevel, scope.TreasuryLaunchpads)
					if e != nil {
						readErr = e
					} else {
						readErr = j.collect(attemptCtx, req.Identity.Contract, scope, chain, nil, nil, nil, at, true)
					}
				}
				if readErr != nil {
					var inputErr mvkt.SupplyError
					if !errors.As(readErr, &inputErr) {
						cancel()
						err = readErr
						break
					}
					reason = mvkt.SupplyReason(readErr)
				}
			}
		}
		cancel()
		if err = j.Store.AdvanceBackfill(ctx, state, &at, reason); err != nil {
			break
		}
		state.NextLevel += state.Step
		state.Complete = state.NextLevel > state.ThroughLevel
	}
	// Aggregate refresh has its own durable watermark. A crash after a
	// checkpoint cannot permanently hide materialized historical buckets.
	through := state.NextLevel - state.Step
	if through >= state.FromLevel && state.RefreshedThroughLevel < through {
		refreshFrom := max(state.FromLevel, state.RefreshedThroughLevel+state.Step)
		first, refreshErr := j.Chain.GetBlockIdentity(ctx, refreshFrom)
		if refreshErr == nil {
			last, e := j.Chain.GetBlockIdentity(ctx, through)
			refreshErr = e
			if refreshErr == nil {
				refreshErr = j.Store.Refresh(ctx, first.Timestamp.UTC().Truncate(24*time.Hour), last.Timestamp.UTC().Truncate(24*time.Hour).Add(24*time.Hour))
			}
		}
		if refreshErr == nil {
			refreshErr = j.Store.MarkBackfillRefreshed(ctx, state.JobID, through)
			if refreshErr == nil {
				state.RefreshedThroughLevel = through
			}
		}
		if err == nil {
			err = refreshErr
		}
	}
	report.State = state
	var scopeErr error
	report.Coverage, scopeErr = j.Store.BackfillCoverage(ctx, req.JobID)
	if err == nil {
		err = scopeErr
	}
	return report, err
}
