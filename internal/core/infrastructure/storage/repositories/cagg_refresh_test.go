package repositories

import (
	"context"
	"strings"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
)

func TestRefreshContinuousAggregates_DegenerateWindow_NoOp(t *testing.T) {
	// A zero or inverted window must return nil before ever touching the DB, so a
	// nil *gorm.DB here also proves no DB access happens (and no panic). This
	// keeps the best-effort refresh from erroring the backfill step on an empty
	// batch window.
	ctx := context.Background()
	zero := time.Time{}
	if err := refreshContinuousAggregates(ctx, nil, tokenCandleAggregates, zero, zero); err != nil {
		t.Errorf("zero window: err = %v, want nil", err)
	}
	t1 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := refreshContinuousAggregates(ctx, nil, rwaCandleAggregates, t1, t0); err != nil {
		t.Errorf("inverted window (from>to): err = %v, want nil", err)
	}
}

func TestCandleAggregateViewNames(t *testing.T) {
	// Guard the closed set that gets string-interpolated into the CALL. If a
	// migration renames a view, this pins the mismatch at the unit level.
	wantToken := []string{"token_prices_1m", "token_prices_1h", "token_prices_1d"}
	wantRWA := []string{"rwa_quote_prices_1m", "rwa_quote_prices_1h", "rwa_quote_prices_1d"}
	if strings.Join(tokenCandleAggregates, ",") != strings.Join(wantToken, ",") {
		t.Errorf("tokenCandleAggregates = %v, want %v", tokenCandleAggregates, wantToken)
	}
	if strings.Join(rwaCandleAggregates, ",") != strings.Join(wantRWA, ",") {
		t.Errorf("rwaCandleAggregates = %v, want %v", rwaCandleAggregates, wantRWA)
	}
}

func TestBuildRWAChangeSQL_NowOnly_EmptyPeriods(t *testing.T) {
	// Regression companion to the token side: a now-only refresh (Periods empty)
	// must produce just the 'now' branch, and GetChange must not early-return —
	// otherwise /rwa/:symbol/change renders now=null once the now-TTL lapses.
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "42",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    nil,
		Now:        now,
	}
	sql, args, err := buildRWAChangeSQL(q, 42, "last")
	if err != nil {
		t.Fatalf("buildRWAChangeSQL: %v", err)
	}
	if strings.Contains(sql, "UNION ALL") {
		t.Errorf("now-only SQL must contain no UNION ALL branch:\n%s", sql)
	}
	if got := strings.Count(sql, "FROM rwa_quote_prices"); got != 1 {
		t.Errorf("FROM rwa_quote_prices count = %d, want 1 (now branch only)", got)
	}
	// Args for the now branch only: pair_id, side.
	if len(args) != 2 {
		t.Errorf("len(args) = %d, want 2 (pid, side)", len(args))
	}
}
