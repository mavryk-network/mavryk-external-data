package prices

import (
	"testing"
	"time"
)

func TestParsePeriod(t *testing.T) {
	cases := []struct {
		in    string
		want  Period
		wantK bool
	}{
		{"1h", Period1h, true},
		{"24h", Period24h, true},
		{"7d", Period7d, true},
		{"30d", Period30d, true},
		{"24H", Period24h, true},
		{"  7d  ", Period7d, true},
		{"7D", Period7d, true},
		{"", "", false},
		{"12h", "", false},
		{"1d", "", false},
		{"random", "", false},
	}
	for _, c := range cases {
		got, ok := ParsePeriod(c.in)
		if got != c.want || ok != c.wantK {
			t.Errorf("ParsePeriod(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantK)
		}
	}
}

func TestPeriodDuration(t *testing.T) {
	cases := []struct {
		p    Period
		want time.Duration
		ok   bool
	}{
		{Period1h, time.Hour, true},
		{Period24h, 24 * time.Hour, true},
		{Period7d, 7 * 24 * time.Hour, true},
		{Period30d, 30 * 24 * time.Hour, true},
		{Period(""), 0, false},
		{Period("12h"), 0, false},
	}
	for _, c := range cases {
		got, ok := c.p.Duration()
		if got != c.want || ok != c.ok {
			t.Errorf("(%q).Duration() = (%v,%v), want (%v,%v)", c.p, got, ok, c.want, c.ok)
		}
	}
}

func TestPeriodStalenessBudget(t *testing.T) {
	cases := []struct {
		p    Period
		want time.Duration
		ok   bool
	}{
		{Period1h, 6 * time.Minute, true},
		{Period24h, 1 * time.Hour, true},
		{Period7d, 12 * time.Hour, true},
		{Period30d, 12 * time.Hour, true},
		{Period(""), 0, false},
		{Period("12h"), 0, false},
	}
	for _, c := range cases {
		got, ok := c.p.StalenessBudget()
		if got != c.want || ok != c.ok {
			t.Errorf("(%q).StalenessBudget() = (%v,%v), want (%v,%v)", c.p, got, ok, c.want, c.ok)
		}
	}
}

func TestPeriodAnchorWindow(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		p   Period
		lo  time.Time
		hi  time.Time
		ok  bool
		msg string
	}{
		{
			p:   Period1h,
			lo:  now.Add(-1 * time.Hour).Add(-6 * time.Minute),
			hi:  now.Add(-1 * time.Hour),
			ok:  true,
			msg: "1h: hi=now-1h, 6m staleness",
		},
		{
			p:   Period24h,
			lo:  now.Add(-24 * time.Hour).Add(-1 * time.Hour),
			hi:  now.Add(-24 * time.Hour),
			ok:  true,
			msg: "24h: hi=now-24h, 1h staleness",
		},
		{
			p:   Period7d,
			lo:  now.Add(-7 * 24 * time.Hour).Add(-12 * time.Hour),
			hi:  now.Add(-7 * 24 * time.Hour),
			ok:  true,
			msg: "7d: hi=now-7d, 12h staleness",
		},
		{
			p:   Period30d,
			lo:  now.Add(-30 * 24 * time.Hour).Add(-12 * time.Hour),
			hi:  now.Add(-30 * 24 * time.Hour),
			ok:  true,
			msg: "30d: hi=now-30d, 12h staleness",
		},
		{p: Period(""), ok: false, msg: "zero period"},
		{p: Period("12h"), ok: false, msg: "unknown period"},
	}
	for _, c := range cases {
		lo, hi, ok := c.p.AnchorWindow(now)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.msg, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if !lo.Equal(c.lo) || !hi.Equal(c.hi) {
			t.Errorf("%s: (lo,hi) = (%v,%v), want (%v,%v)", c.msg, lo, hi, c.lo, c.hi)
		}
		if !lo.Before(hi) {
			t.Errorf("%s: lo (%v) must be before hi (%v)", c.msg, lo, hi)
		}
		// Symmetry check: hi must equal exactly now - period.
		dur, _ := c.p.Duration()
		if !hi.Equal(now.Add(-dur)) {
			t.Errorf("%s: hi (%v) must equal now-period (%v) for symmetric anchor semantics",
				c.msg, hi, now.Add(-dur))
		}
	}
}

func TestAllPeriodsOrdering(t *testing.T) {
	// The order matters for default response shape; assert it explicitly so
	// future "alphabetical sort" refactors fail loudly.
	want := []Period{Period1h, Period24h, Period7d, Period30d}
	if len(AllPeriods) != len(want) {
		t.Fatalf("AllPeriods len = %d, want %d", len(AllPeriods), len(want))
	}
	for i, p := range want {
		if AllPeriods[i] != p {
			t.Errorf("AllPeriods[%d] = %q, want %q", i, AllPeriods[i], p)
		}
	}
}

func TestDefaultChangePeriods(t *testing.T) {
	want := []Period{Period24h, Period7d, Period30d}
	if len(DefaultChangePeriods) != len(want) {
		t.Fatalf("DefaultChangePeriods len = %d, want %d", len(DefaultChangePeriods), len(want))
	}
	for i, p := range want {
		if DefaultChangePeriods[i] != p {
			t.Errorf("DefaultChangePeriods[%d] = %q, want %q", i, DefaultChangePeriods[i], p)
		}
	}
}
