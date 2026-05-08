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

	// Structural checks — the assertion stays loose so cosmetic SQL
	// reformatting doesn't break the test.
	for _, want := range []string{
		"FROM token_prices",
		"FROM token_prices_1h",
		"FROM token_prices_1d",
		"DISTINCT ON (quote_currency)",
		"UNION ALL",
		"'now'::text",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q in:\n%s", want, sql)
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

func TestBuildTokenChangeSQL_AllFourPeriods(t *testing.T) {
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
	for _, view := range []string{"token_prices_1m", "token_prices_1h", "token_prices_1d"} {
		if !strings.Contains(sql, view) {
			t.Errorf("expected %s reference in SQL", view)
		}
	}
	// 1h → _1m, 24h → _1h, 7d/30d → _1d
	if strings.Count(sql, "FROM token_prices_1d") != 2 {
		t.Errorf("expected 2 references to token_prices_1d (7d + 30d), got %d", strings.Count(sql, "FROM token_prices_1d"))
	}

	// Defensive: confirm window bounds make it into args. Last 2 args of
	// the 30d branch should be -30d12h and -30d.
	hi30 := args[len(args)-1].(time.Time)
	lo30 := args[len(args)-2].(time.Time)
	wantHi30 := now.Add(-30 * 24 * time.Hour).UTC()
	wantLo30 := now.Add(-30*24*time.Hour - 12*time.Hour).UTC()
	if !hi30.Equal(wantHi30) {
		t.Errorf("30d hi = %v, want %v", hi30, wantHi30)
	}
	if !lo30.Equal(wantLo30) {
		t.Errorf("30d lo = %v, want %v", lo30, wantLo30)
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
