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

	for _, want := range []string{
		"FROM rwa_quote_prices",
		"FROM rwa_quote_prices_1h",
		"FROM rwa_quote_prices_1d",
		"pair_id = ?",
		"side = ?",
		"'now'::text",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q", want)
		}
	}

	// 7d and 30d both hit _1d → 2 references
	if c := strings.Count(sql, "FROM rwa_quote_prices_1d"); c != 2 {
		t.Errorf("rwa_quote_prices_1d count = %d, want 2", c)
	}

	// 1 anchor branch per period
	if c := strings.Count(sql, "UNION ALL"); c != len(q.Periods) {
		t.Errorf("UNION ALL count = %d, want %d", c, len(q.Periods))
	}

	// Param count: now branch (pid, side) + per period (period_label, pid, side, lo, hi).
	wantArgs := 2 + len(q.Periods)*5
	if len(args) != wantArgs {
		t.Errorf("len(args) = %d, want %d", len(args), wantArgs)
	}
}

func TestBuildRWAChangeSQL_AllFourPeriods_BackingCAMapping(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "1",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period1h, prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	}
	sql, _, err := buildRWAChangeSQL(q, 1, "last")
	if err != nil {
		t.Fatalf("buildRWAChangeSQL: %v", err)
	}
	if !strings.Contains(sql, "FROM rwa_quote_prices_1m") {
		t.Error("expected rwa_quote_prices_1m for 1h period")
	}
	if !strings.Contains(sql, "FROM rwa_quote_prices_1h") {
		t.Error("expected rwa_quote_prices_1h for 24h period")
	}
	if c := strings.Count(sql, "FROM rwa_quote_prices_1d"); c != 2 {
		t.Errorf("expected 2× rwa_quote_prices_1d for 7d+30d, got %d", c)
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
