package httpclient

import "testing"

func TestRateLimitSettings_Normalized(t *testing.T) {
	cases := []struct {
		name string
		in   RateLimitSettings
		want RateLimitSettings
	}{
		{"disabled", RateLimitSettings{RPS: 0}, RateLimitSettings{RPS: 0}},
		{"explicit_burst", RateLimitSettings{RPS: 8, Burst: 16}, RateLimitSettings{RPS: 8, Burst: 16}},
		{"auto_burst_int_rps", RateLimitSettings{RPS: 8}, RateLimitSettings{RPS: 8, Burst: 16}},
		{"auto_burst_fractional_rps", RateLimitSettings{RPS: 0.5}, RateLimitSettings{RPS: 0.5, Burst: 1}},
		{"auto_burst_negative_burst", RateLimitSettings{RPS: 8, Burst: -3}, RateLimitSettings{RPS: 8, Burst: 16}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Normalized()
			if got.RPS != c.want.RPS || got.Burst != c.want.Burst {
				t.Errorf("Normalized(%+v) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestRateLimitSettings_Enabled(t *testing.T) {
	cases := []struct {
		in   RateLimitSettings
		want bool
	}{
		{RateLimitSettings{}, false},
		{RateLimitSettings{RPS: 0.5}, true}, // burst auto-corrected
		{RateLimitSettings{RPS: 0.5, Burst: 0}, true},
		{RateLimitSettings{RPS: 0.5, Burst: -1}, true},
		{RateLimitSettings{RPS: 0, Burst: 5}, false}, // no rate, no limiter
	}
	for _, c := range cases {
		if got := c.in.Enabled(); got != c.want {
			t.Errorf("Enabled(%+v) = %v, want %v", c.in, got, c.want)
		}
	}
}
