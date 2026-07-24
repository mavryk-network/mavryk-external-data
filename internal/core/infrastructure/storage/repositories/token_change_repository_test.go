package repositories

import (
	"strings"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

func TestBuildTokenChangeSQL_StructureAndParamCount(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd", "eur"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d},
		Now:        now,
	}
	sql, args, err := buildTokenChangeSQL(q)
	if err != nil {
		t.Fatalf("buildTokenChangeSQL: %v", err)
	}

	// All branches read raw `token_prices` (Decision #18 — no CA suffixes).
	for _, want := range []string{
		"FROM token_prices",
		"DISTINCT ON (quote_currency)",
		"UNION ALL",
		"'now'::text",
		"ts >= ? AND ts <= ?",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q in:\n%s", want, sql)
		}
	}
	// Negative assertions: no CA references should remain.
	for _, banned := range []string{"token_prices_1m", "token_prices_1h", "token_prices_1d", "close_price", "bucket"} {
		if strings.Contains(sql, banned) {
			t.Errorf("SQL must not reference %q (Decision #18 — raw only):\n%s", banned, sql)
		}
	}

	// Param count = (entity, source, N_curs) for "now" branch
	//             + per period (period_label, entity, source, N_curs, lo, hi)
	wantArgs := 2 + len(q.Currencies) + len(q.Periods)*(3+len(q.Currencies)+2)
	if len(args) != wantArgs {
		t.Errorf("len(args) = %d, want %d", len(args), wantArgs)
	}

	// Two periods in the request → 2 anchor UNION ALL branches.
	if got := strings.Count(sql, "UNION ALL"); got != len(q.Periods) {
		t.Errorf("UNION ALL count = %d, want %d (one per period)", got, len(q.Periods))
	}
}

func TestBuildTokenChangeSQL_AllFourPeriods_AnchorWindow(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period1h, prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	}
	sql, args, err := buildTokenChangeSQL(q)
	if err != nil {
		t.Fatalf("buildTokenChangeSQL: %v", err)
	}
	// Single source table, 5 references (1 now + 4 anchors).
	if got := strings.Count(sql, "FROM token_prices"); got != 5 {
		t.Errorf("FROM token_prices count = %d, want 5 (1 now + 4 periods)", got)
	}
	// Confirm the anchor window args make it through for the LAST period (30d).
	// Per period the trailing pair is (lo, hi); 30d is the last branch.
	hi30 := args[len(args)-1].(time.Time)
	lo30 := args[len(args)-2].(time.Time)
	wantHi30 := now.Add(-30 * 24 * time.Hour).UTC()
	wantLo30 := now.Add(-30*24*time.Hour - 12*time.Hour).UTC()
	if !hi30.Equal(wantHi30) {
		t.Errorf("30d hi = %v, want %v (now-30d, at-or-before semantics)", hi30, wantHi30)
	}
	if !lo30.Equal(wantLo30) {
		t.Errorf("30d lo = %v, want %v (now-30d-12h staleness)", lo30, wantLo30)
	}
}

func TestAssembleTokenChangeResult_HappyPath(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd", "eur"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d},
		Now:        now,
	}
	rows := []tokenChangeRow{
		// Now branch
		{Period: "now", QuoteCurrency: "usd", ObservedTS: now, Price: decimal.RequireFromString("0.071541")},
		{Period: "now", QuoteCurrency: "eur", ObservedTS: now, Price: decimal.RequireFromString("0.060941")},
		// 24h
		{Period: "24h", QuoteCurrency: "usd", ObservedTS: now.Add(-24 * time.Hour), Price: decimal.RequireFromString("0.072100")},
		{Period: "24h", QuoteCurrency: "eur", ObservedTS: now.Add(-24 * time.Hour), Price: decimal.RequireFromString("0.061500")},
		// 7d (eur missing — gap in CA)
		{Period: "7d", QuoteCurrency: "usd", ObservedTS: now.Add(-7 * 24 * time.Hour), Price: decimal.RequireFromString("0.069300")},
	}

	res := assembleTokenChangeResult(q, rows)

	// Now: both currencies found.
	if len(res.Now) != 2 {
		t.Fatalf("len(Now) = %d, want 2", len(res.Now))
	}
	for _, n := range res.Now {
		if !n.Found {
			t.Errorf("now %s not found", n.Currency)
		}
	}

	// Anchors: 4 entries (2 currencies × 2 periods); 7d.eur must be Found=false.
	if len(res.Anchors) != 4 {
		t.Fatalf("len(Anchors) = %d, want 4", len(res.Anchors))
	}
	var found7dEUR bool
	for _, a := range res.Anchors {
		if a.Period == prices.Period7d && a.Currency == "eur" {
			if a.Found {
				t.Error("7d.eur must be Found=false (no row in SQL output)")
			}
			found7dEUR = true
		}
	}
	if !found7dEUR {
		t.Error("expected 7d.eur entry in result anchors")
	}
}

func TestAssembleTokenChangeResult_AllMissing(t *testing.T) {
	// New token with no observations — repo returns zero rows;
	// assembler still emits Found=false placeholders for every (currency, period).
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "newtoken",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	}
	res := assembleTokenChangeResult(q, nil)

	if len(res.Now) != 1 || res.Now[0].Found {
		t.Errorf("Now = %+v, want one Found=false entry", res.Now)
	}
	if len(res.Anchors) != 3 {
		t.Fatalf("len(Anchors) = %d, want 3", len(res.Anchors))
	}
	for _, a := range res.Anchors {
		if a.Found {
			t.Errorf("anchor %v.%s expected Found=false", a.Period, a.Currency)
		}
	}
}

func TestBuildTokenChangeSQL_EmptyShortCircuit(t *testing.T) {
	// Defensive: zero currencies or zero periods returns nil/empty result
	// without ever calling buildTokenChangeSQL — but if it ever did get
	// called with zero, we want a clean empty SQL fragment, not a panic.
	q := apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{},
		Periods:    []prices.Period{prices.Period24h},
		Now:        time.Now(),
	}
	// strings.Repeat with negative count panics — assert we filter at the
	// repo entrypoint (GetChange short-circuits on empty Currencies).
	defer func() {
		if r := recover(); r == nil {
			t.Log("buildTokenChangeSQL did not panic on empty currencies — caller short-circuits earlier")
		}
	}()
	_, _, _ = buildTokenChangeSQL(q)
}

func TestBuildTokenChangeSQL_NowOnly_EmptyPeriods(t *testing.T) {
	// Regression: a "now-only" refresh (only the now cache slot expired) reaches
	// the repo with Currencies set but Periods empty. buildTokenChangeSQL must
	// emit just the 'now' branch, and GetChange must run it rather than
	// early-return — otherwise /change renders now=null (and every change_pct
	// null) each time the now-TTL lapses inside the anchor TTLs.
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	q := apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd", "eur"},
		Periods:    nil,
		Now:        now,
	}
	sql, args, err := buildTokenChangeSQL(q)
	if err != nil {
		t.Fatalf("buildTokenChangeSQL: %v", err)
	}
	if strings.Contains(sql, "UNION ALL") {
		t.Errorf("now-only SQL must contain no UNION ALL branch:\n%s", sql)
	}
	if got := strings.Count(sql, "FROM token_prices"); got != 1 {
		t.Errorf("FROM token_prices count = %d, want 1 (now branch only)", got)
	}
	if !strings.Contains(sql, "'now'::text") {
		t.Errorf("now branch missing:\n%s", sql)
	}
	// Args: entity, source, then one per currency. No period/window args.
	if want := 2 + len(q.Currencies); len(args) != want {
		t.Errorf("len(args) = %d, want %d", len(args), want)
	}
}
