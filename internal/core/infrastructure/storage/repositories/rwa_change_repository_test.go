package repositories

import (
	"strings"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

func TestBuildRWAChangeSQL_Structure(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "42",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	}
	sql, args, err := buildRWAChangeSQL(q, 42, "last")
	if err != nil {
		t.Fatalf("buildRWAChangeSQL: %v", err)
	}

	// Decision #18 — raw table only, no CA references.
	for _, want := range []string{
		"FROM rwa_quote_prices",
		"pair_id = ?",
		"side = ?",
		"'now'::text",
		"ts >= ? AND ts <= ?",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q", want)
		}
	}
	for _, banned := range []string{"rwa_quote_prices_1m", "rwa_quote_prices_1h", "rwa_quote_prices_1d", "close_price", "bucket"} {
		if strings.Contains(sql, banned) {
			t.Errorf("SQL must not reference %q (Decision #18 — raw only):\n%s", banned, sql)
		}
	}

	// 1 anchor branch per period; one extra "FROM rwa_quote_prices" per period
	// for a total of 1 (now) + N (anchor).
	if got := strings.Count(sql, "FROM rwa_quote_prices"); got != 1+len(q.Periods) {
		t.Errorf("FROM rwa_quote_prices count = %d, want %d", got, 1+len(q.Periods))
	}
	if c := strings.Count(sql, "UNION ALL"); c != len(q.Periods) {
		t.Errorf("UNION ALL count = %d, want %d", c, len(q.Periods))
	}

	// Param count: now branch (pid, side) + per period (period_label, pid, side, lo, hi).
	wantArgs := 2 + len(q.Periods)*5
	if len(args) != wantArgs {
		t.Errorf("len(args) = %d, want %d", len(args), wantArgs)
	}
}

func TestBuildRWAChangeSQL_AnchorWindowsTrailing(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "1",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period1h, prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	}
	_, args, err := buildRWAChangeSQL(q, 1, "last")
	if err != nil {
		t.Fatalf("buildRWAChangeSQL: %v", err)
	}
	// Last branch (30d): trailing args are (period_label, pid, side, lo, hi).
	hi := args[len(args)-1].(time.Time)
	lo := args[len(args)-2].(time.Time)
	if !hi.Equal(now.Add(-30 * 24 * time.Hour).UTC()) {
		t.Errorf("30d hi = %v, want now-30d", hi)
	}
	if !lo.Equal(now.Add(-30*24*time.Hour - 12*time.Hour).UTC()) {
		t.Errorf("30d lo = %v, want now-30d-12h", lo)
	}
}

func TestAssembleRWAChangeResult_HappyPath(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "42",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d},
		Now:        now,
	}
	rows := []rwaChangeRow{
		{Period: "now", ObservedTS: now, Price: decimal.RequireFromString("56.25")},
		{Period: "24h", ObservedTS: now.Add(-24 * time.Hour), Price: decimal.RequireFromString("55.80")},
		// 7d row missing — anchor should land Found=false
	}
	res := assembleRWAChangeResult(q, rows)

	if len(res.Now) != 1 || !res.Now[0].Found {
		t.Fatalf("Now = %+v", res.Now)
	}
	if res.Now[0].Currency != "usdt" {
		t.Errorf("now currency = %q, want usdt", res.Now[0].Currency)
	}
	if !res.Now[0].Price.Equal(decimal.RequireFromString("56.25")) {
		t.Errorf("now price = %s", res.Now[0].Price)
	}
	if len(res.Anchors) != 2 {
		t.Fatalf("len(Anchors) = %d, want 2", len(res.Anchors))
	}
	for _, a := range res.Anchors {
		switch a.Period {
		case prices.Period24h:
			if !a.Found {
				t.Error("24h must be found")
			}
		case prices.Period7d:
			if a.Found {
				t.Error("7d must NOT be found")
			}
		}
		if a.Currency != "usdt" {
			t.Errorf("anchor %v currency = %q, want usdt", a.Period, a.Currency)
		}
	}
}

func TestAssembleRWAChangeResult_EmptyRows(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "42",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h, prices.Period30d},
		Now:        now,
	}
	res := assembleRWAChangeResult(q, nil)
	if len(res.Now) != 1 || res.Now[0].Found {
		t.Errorf("expected one Found=false now, got %+v", res.Now)
	}
	if len(res.Anchors) != 2 {
		t.Fatalf("len(Anchors) = %d", len(res.Anchors))
	}
	for _, a := range res.Anchors {
		if a.Found {
			t.Errorf("anchor %v expected Found=false", a.Period)
		}
	}
}
