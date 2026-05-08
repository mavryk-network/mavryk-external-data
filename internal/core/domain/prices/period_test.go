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

func TestPeriodBackingCA(t *testing.T) {
	cases := []struct {
		p    Period
		want string
	}{
		{Period1h, "_1m"},
		{Period24h, "_1h"},
		{Period7d, "_1d"},
		{Period30d, "_1d"},
		{Period(""), ""},
		{Period("12h"), ""},
	}
	for _, c := range cases {
		if got := c.p.BackingCA(); got != c.want {
			t.Errorf("(%q).BackingCA() = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestPeriodToleranceWindow(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		p   Period
		lo  time.Time
		hi  time.Time
		ok  bool
		msg string
	}{
		{Period1h, now.Add(-66 * time.Minute), now.Add(-60 * time.Minute), true, "1h: 6-minute slack"},
		{Period24h, now.Add(-25 * time.Hour), now.Add(-24 * time.Hour), true, "24h: 1-hour slack"},
		{Period7d, now.Add(-7*24*time.Hour - 12*time.Hour), now.Add(-7 * 24 * time.Hour), true, "7d: 12-hour slack"},
		{Period30d, now.Add(-30*24*time.Hour - 12*time.Hour), now.Add(-30 * 24 * time.Hour), true, "30d: 12-hour slack"},
		{Period(""), time.Time{}, time.Time{}, false, "zero period"},
		{Period("12h"), time.Time{}, time.Time{}, false, "unknown period"},
	}
	for _, c := range cases {
		lo, hi, ok := c.p.ToleranceWindow(now)
		if !lo.Equal(c.lo) || !hi.Equal(c.hi) || ok != c.ok {
			t.Errorf("%s: (%q).ToleranceWindow(now) = (%v,%v,%v), want (%v,%v,%v)",
				c.msg, c.p, lo, hi, ok, c.lo, c.hi, c.ok)
		}
		if ok && !lo.Before(hi) {
			t.Errorf("%s: lo (%v) must be before hi (%v)", c.msg, lo, hi)
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
