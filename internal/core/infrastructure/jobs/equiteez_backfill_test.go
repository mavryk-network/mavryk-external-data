package jobs

import (
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"
)

func TestOrdersToLastPoints_NormalizesPriceAndTimestamp(t *testing.T) {
	pair := prices.RWAPair{
		ID:          7,
		Source:      prices.SourceEquiteez,
		BaseSymbol:  "TST",
		QuoteSymbol: "USDT",
	}
	orders := []equiteez.OrderbookOrder{
		// micro-USDT 56_250_000 → 56.25 USDT (decimals=6).
		{ID: 1, OrderType: 1, PricePerRWAToken: 56_250_000, FulfilledAmount: 1_000_000, EndedAt: "2025-08-08T14:42:10Z"},
		// 1_000_000 → 1.00 USDT.
		{ID: 2, OrderType: 0, PricePerRWAToken: 1_000_000, FulfilledAmount: 5_000_000, EndedAt: "2025-08-09T00:00:00Z"},
	}
	pts := ordersToLastPoints(pair, orders, 6)
	if len(pts) != 2 {
		t.Fatalf("len(pts) = %d, want 2", len(pts))
	}
	want := map[int64]string{1: "56.25", 2: "1"}
	for i, p := range pts {
		if got := p.Price.String(); got != want[orders[i].ID] {
			t.Errorf("orders[%d] price = %q, want %q", i, got, want[orders[i].ID])
		}
		if p.Metric != string(prices.SideLast) {
			t.Errorf("orders[%d] metric = %q, want %q", i, p.Metric, prices.SideLast)
		}
		if p.EntityKey != "7" {
			t.Errorf("orders[%d] entity_key = %q, want %q", i, p.EntityKey, "7")
		}
		if p.Source != prices.SourceEquiteez {
			t.Errorf("orders[%d] source = %q, want %q", i, p.Source, prices.SourceEquiteez)
		}
	}
	wantTS := time.Date(2025, 8, 8, 14, 42, 10, 0, time.UTC)
	if !pts[0].Timestamp.Equal(wantTS) {
		t.Errorf("pts[0].ts = %s, want %s", pts[0].Timestamp, wantTS)
	}
}

func TestOrdersToLastPoints_SkipsBadRows(t *testing.T) {
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez, QuoteSymbol: "USDT"}
	orders := []equiteez.OrderbookOrder{
		{ID: 1, PricePerRWAToken: 0, EndedAt: "2025-08-08T00:00:00Z"},  // zero price → skip
		{ID: 2, PricePerRWAToken: -5, EndedAt: "2025-08-08T00:00:00Z"}, // negative → skip
		{ID: 3, PricePerRWAToken: 1_000_000, EndedAt: "garbage"},       // bad ts → skip
		{ID: 4, PricePerRWAToken: 1_000_000, EndedAt: "2025-08-09T00:00:00Z"},
	}
	pts := ordersToLastPoints(pair, orders, 6)
	if len(pts) != 1 {
		t.Fatalf("len(pts) = %d, want 1 (only id=4 valid)", len(pts))
	}
	if pts[0].Price.String() != "1" {
		t.Errorf("price = %q, want \"1\"", pts[0].Price.String())
	}
}

func TestOrdersToLastPoints_DedupesEqualTimestamps(t *testing.T) {
	pair := prices.RWAPair{ID: 9, Source: prices.SourceEquiteez, QuoteSymbol: "USDT"}
	// Three orders share the same ended_at; orders arrive id-ASC so the later
	// id (= more recent fill) must win after dedup.
	orders := []equiteez.OrderbookOrder{
		{ID: 10, PricePerRWAToken: 50_000_000, EndedAt: "2025-08-08T14:42:10Z"}, // 50.00
		{ID: 11, PricePerRWAToken: 51_000_000, EndedAt: "2025-08-08T14:42:10Z"}, // 51.00 (collision)
		{ID: 12, PricePerRWAToken: 60_000_000, EndedAt: "2025-08-09T00:00:00Z"}, // 60.00 (different ts, kept)
		{ID: 13, PricePerRWAToken: 52_000_000, EndedAt: "2025-08-08T14:42:10Z"}, // 52.00 (last collision wins)
	}
	pts := ordersToLastPoints(pair, orders, 6)
	if len(pts) != 2 {
		t.Fatalf("len = %d, want 2 (one per distinct ts)", len(pts))
	}
	// Distinct timestamps: insertion order preserved at first occurrence.
	if pts[0].Timestamp.Format(time.RFC3339) != "2025-08-08T14:42:10Z" {
		t.Errorf("pts[0].ts = %s, want 2025-08-08T14:42:10Z", pts[0].Timestamp)
	}
	if pts[0].Price.String() != "52" {
		t.Errorf("pts[0].price = %q, want \"52\" (latest id at the colliding ts)", pts[0].Price.String())
	}
	if pts[1].Timestamp.Format(time.RFC3339) != "2025-08-09T00:00:00Z" {
		t.Errorf("pts[1].ts = %s, want 2025-08-09T00:00:00Z", pts[1].Timestamp)
	}
	if pts[1].Price.String() != "60" {
		t.Errorf("pts[1].price = %q, want \"60\"", pts[1].Price.String())
	}
}

func TestOrdersToLastPoints_NoNormalizationWhenDecimalsZero(t *testing.T) {
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez}
	orders := []equiteez.OrderbookOrder{
		{ID: 1, PricePerRWAToken: 100, EndedAt: "2025-01-01T00:00:00Z"},
	}
	pts := ordersToLastPoints(pair, orders, 0)
	if len(pts) != 1 {
		t.Fatalf("len(pts) = %d, want 1", len(pts))
	}
	if pts[0].Price.String() != "100" {
		t.Errorf("price = %q, want \"100\" (no shift when decimals=0)", pts[0].Price.String())
	}
}

func TestMaxOrderID(t *testing.T) {
	cases := []struct {
		name string
		ids  []int64
		want int64
	}{
		{"single", []int64{42}, 42},
		{"ascending", []int64{1, 2, 3, 5, 8}, 8},
		{"descending", []int64{8, 5, 3, 2, 1}, 8},
		{"random", []int64{3, 1, 9, 4, 1, 5}, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orders := make([]equiteez.OrderbookOrder, len(c.ids))
			for i, id := range c.ids {
				orders[i] = equiteez.OrderbookOrder{ID: id}
			}
			if got := maxOrderID(orders); got != c.want {
				t.Errorf("maxOrderID = %d, want %d", got, c.want)
			}
		})
	}
}

func TestFilterBackfillablePairs(t *testing.T) {
	in := []prices.RWAPair{
		{ID: 3, Source: prices.SourceEquiteez, Enabled: true},
		{ID: 1, Source: prices.SourceEquiteez, Enabled: false},
		{ID: 2, Source: prices.SourceEquiteez, Enabled: true},
		{ID: 5, Source: prices.SourceCoinGecko, Enabled: true}, // wrong source
	}
	out := filterBackfillablePairs(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (id=2 and id=3, sorted)", len(out))
	}
	if out[0].ID != 2 || out[1].ID != 3 {
		t.Errorf("order = [%d, %d], want [2, 3]", out[0].ID, out[1].ID)
	}
}

// TestPauseUntilNextFill pins the fix for the sticky `caught_up` disable: a
// forward-walking backfill that reaches the head of the log must PAUSE, never
// disable. Disabling was permanent (backfill_state survives restarts), so fills
// arriving during downtime were never ingested and a pair that had not traded
// yet was killed on its first tick.
func TestPauseUntilNextFill(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cursor := int64(4242)
	st := &repositories.BackfillState{
		Source:    prices.SourceEquiteez,
		EntityKey: "7",
		CursorID:  &cursor,
		// Simulated leftovers from an earlier transient failure.
		ErrorCount: 3,
		LastError:  "boom",
	}

	pauseUntilNextFill(st, now)

	if st.Disabled {
		t.Error("catching up must not disable the pair (forward walk has no terminal state)")
	}
	if st.DisabledReason != "" {
		t.Errorf("DisabledReason = %q, want empty", st.DisabledReason)
	}
	if st.NextAttemptAt == nil {
		t.Fatal("NextAttemptAt must be set so the pair is re-checked")
	}
	if want := now.Add(caughtUpRecheckInterval); !st.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v", *st.NextAttemptAt, want)
	}
	if st.NextAttemptAt.Before(now) {
		t.Error("NextAttemptAt must be in the future, otherwise the pause is a no-op")
	}
	// Reaching the head of the log is success — error bookkeeping resets so the
	// pair cannot drift into the auto-disable threshold while merely idle.
	if st.ErrorCount != 0 || st.LastError != "" {
		t.Errorf("error bookkeeping not reset: count=%d last=%q", st.ErrorCount, st.LastError)
	}
	// The cursor must survive: resuming re-walks from where we stopped rather
	// than replaying the whole history.
	if st.CursorID == nil || *st.CursorID != 4242 {
		t.Errorf("CursorID = %v, want 4242 preserved", st.CursorID)
	}
}
