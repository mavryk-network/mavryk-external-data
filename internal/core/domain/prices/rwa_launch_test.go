package prices

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	v, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("bad decimal %q: %v", s, err)
	}
	return v
}

func TestProgressPercent(t *testing.T) {
	cases := []struct {
		name       string
		total, cap string
		want       float64
	}{
		// KHBE in production: a barely-started launch must stay visible rather
		// than flattening to 0 — the whole reason big.Float is used here.
		{"tiny real-world value", "6667", "2500000000000", 2.667e-7},
		{"half", "50", "100", 50},
		{"full", "100", "100", 100},
		{"zero bought", "0", "100", 0},
		{"zero cap -> 0 not NaN/Inf", "10", "0", 0},
		{"empty cap", "10", "", 0},
		{"garbage", "abc", "100", 0},
		// Beyond float64's 2^53: an 18-decimal token's supply scale.
		{"supply-scale", "1000000000000000000000", "2000000000000000000000", 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProgressPercent(c.total, c.cap)
			if diff := got - c.want; diff > 1e-12 || diff < -1e-12 {
				t.Errorf("ProgressPercent(%q,%q) = %v, want %v", c.total, c.cap, got, c.want)
			}
		})
	}
}

func TestLaunchHumanPrice(t *testing.T) {
	// 100000000 raw USDT (6 decimals) is the KHBE base tier → 100.
	if v, ok := LaunchHumanPrice("100000000", 6); !ok || !v.Equal(dec(t, "100")) {
		t.Errorf("got %v (ok=%v), want 100", v, ok)
	}
	if v, ok := LaunchHumanPrice("1250640000", 6); !ok || !v.Equal(dec(t, "1250.64")) {
		t.Errorf("MCDX starter: got %v (ok=%v), want 1250.64", v, ok)
	}
	if _, ok := LaunchHumanPrice("", 6); ok {
		t.Error("empty raw must not be ok")
	}
	if _, ok := LaunchHumanPrice("abc", 6); ok {
		t.Error("garbage raw must not be ok")
	}
	if _, ok := LaunchHumanPrice("100", 41); ok {
		t.Error("out-of-range decimals must not be ok (guards the exponentiation cost)")
	}
}

func TestLaunchActiveAndSelect(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	closed := now.Add(-time.Minute)

	active := LaunchSelectable{Status: LaunchStatusActive, SaleStart: &past, SaleEnd: &future, UpdatedAt: now.Add(-48 * time.Hour)}
	if !LaunchActive(active, now) {
		t.Error("in-window active launch must be purchasable")
	}
	if LaunchActive(LaunchSelectable{Status: LaunchStatusActive, IsPaused: true}, now) {
		t.Error("paused launch must not be active")
	}
	if LaunchActive(LaunchSelectable{Status: LaunchStatusActive, SaleClosed: &closed}, now) {
		t.Error("closed launch must not be active")
	}
	if LaunchActive(LaunchSelectable{Status: LaunchStatusActive, SaleStart: &future}, now) {
		t.Error("not-yet-started launch must not be active")
	}
	if LaunchActive(LaunchSelectable{Status: LaunchStatusClosed}, now) {
		t.Error("closed status must not be active")
	}
	// Open-ended window: no bounds means it never expires on its own.
	if !LaunchActive(LaunchSelectable{Status: LaunchStatusActive}, now) {
		t.Error("launch with no window bounds must be active")
	}

	// Selection: an active launch beats a more recently updated inactive one.
	newerInactive := LaunchSelectable{Status: LaunchStatusClosed, UpdatedAt: now}
	idx, ok := SelectLaunch([]LaunchSelectable{newerInactive, active}, now)
	if !ok || idx != 1 {
		t.Errorf("idx = %d (ok=%v), want the active launch at 1", idx, ok)
	}
	// Among equals, newest updated_at wins.
	older := LaunchSelectable{Status: LaunchStatusActive, UpdatedAt: now.Add(-72 * time.Hour)}
	newer := LaunchSelectable{Status: LaunchStatusActive, UpdatedAt: now}
	idx, _ = SelectLaunch([]LaunchSelectable{older, newer}, now)
	if idx != 1 {
		t.Errorf("idx = %d, want 1 (newest updated_at)", idx)
	}
	if _, ok := SelectLaunch(nil, now); ok {
		t.Error("empty slice must return ok=false")
	}
}

func TestLaunchStatusStringAndSymbol(t *testing.T) {
	for code, want := range map[int]string{0: "active", 1: "inactive", 2: "paused", 3: "closed", 99: "unknown"} {
		if got := LaunchStatusString(code); got != want {
			t.Errorf("LaunchStatusString(%d) = %q, want %q", code, got, want)
		}
	}
	l := RWALaunch{BaseSymbol: "KHBE", QuoteSymbol: "USDT"}
	if got := l.Symbol(); got != "khbe-usdt" {
		t.Errorf("Symbol() = %q, want khbe-usdt", got)
	}
}
