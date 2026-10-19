package prices

import (
	"testing"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

func TestChangeCacheNowRoundTrip(t *testing.T) {
	c := NewChangeCache()
	src := prices.SourceCoinGecko
	ent := "mvrk"
	cur := "usd"
	price := decimal.RequireFromString("0.071541")
	ts := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	if _, _, ok := c.GetNow(src, ent, "last", cur); ok {
		t.Fatal("GetNow on empty cache must miss")
	}
	c.SetNow(src, ent, "last", cur, price, ts)
	got, gotTS, ok := c.GetNow(src, ent, "last", cur)
	if !ok {
		t.Fatal("GetNow after SetNow must hit")
	}
	if !got.Equal(price) {
		t.Errorf("price = %s, want %s", got, price)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("ts = %v, want %v", gotTS, ts)
	}
}

func TestChangeCacheAnchorRoundTrip(t *testing.T) {
	c := NewChangeCache()
	src := prices.SourceCoinGecko
	ent := "mvrk"
	cur := "usd"
	per := prices.Period24h
	price := decimal.RequireFromString("0.072100")
	ts := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)

	if _, _, ok := c.GetAnchor(src, ent, "last", cur, per); ok {
		t.Fatal("GetAnchor on empty cache must miss")
	}
	c.SetAnchor(src, ent, "last", cur, per, price, ts)
	got, gotTS, ok := c.GetAnchor(src, ent, "last", cur, per)
	if !ok {
		t.Fatal("GetAnchor after SetAnchor must hit")
	}
	if !got.Equal(price) {
		t.Errorf("price = %s, want %s", got, price)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("ts = %v, want %v", gotTS, ts)
	}
}

func TestChangeCacheNowAndAnchorAreIndependent(t *testing.T) {
	// Storing a "now" entry must not collide with an anchor entry that
	// shares the same (source, entity, currency).
	c := NewChangeCache()
	src := prices.SourceCoinGecko
	ent := "mvrk"
	cur := "usd"
	per := prices.Period24h

	now := decimal.RequireFromString("100")
	anchor := decimal.RequireFromString("90")
	c.SetNow(src, ent, "last", cur, now, time.Now())
	c.SetAnchor(src, ent, "last", cur, per, anchor, time.Now().Add(-24*time.Hour))

	if got, _, _ := c.GetNow(src, ent, "last", cur); !got.Equal(now) {
		t.Errorf("now = %s, want %s", got, now)
	}
	if got, _, _ := c.GetAnchor(src, ent, "last", cur, per); !got.Equal(anchor) {
		t.Errorf("anchor = %s, want %s", got, anchor)
	}
	if c.Len() != 2 {
		t.Errorf("cache len = %d, want 2", c.Len())
	}
}

func TestChangeCachePerPeriodIndependence(t *testing.T) {
	// Storing 24h anchor must not conflict with 7d anchor at same (entity, currency).
	c := NewChangeCache()
	src := prices.SourceCoinGecko
	ent := "mvrk"
	cur := "usd"

	v24 := decimal.RequireFromString("90")
	v7 := decimal.RequireFromString("80")
	c.SetAnchor(src, ent, "last", cur, prices.Period24h, v24, time.Now())
	c.SetAnchor(src, ent, "last", cur, prices.Period7d, v7, time.Now())

	if got, _, _ := c.GetAnchor(src, ent, "last", cur, prices.Period24h); !got.Equal(v24) {
		t.Errorf("24h = %s, want %s", got, v24)
	}
	if got, _, _ := c.GetAnchor(src, ent, "last", cur, prices.Period7d); !got.Equal(v7) {
		t.Errorf("7d = %s, want %s", got, v7)
	}
	// 30d was never set; must miss.
	if _, _, ok := c.GetAnchor(src, ent, "last", cur, prices.Period30d); ok {
		t.Error("30d should miss")
	}
}

func TestTTLFor(t *testing.T) {
	cases := []struct {
		p    prices.Period
		want time.Duration
	}{
		{prices.Period1h, 30 * time.Second},
		{prices.Period24h, 60 * time.Second},
		{prices.Period7d, 5 * time.Minute},
		{prices.Period30d, 15 * time.Minute},
		{prices.Period(""), 30 * time.Second},
		{prices.Period("12h"), 30 * time.Second},
	}
	for _, c := range cases {
		if got := TTLFor(c.p); got != c.want {
			t.Errorf("TTLFor(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestChangeCacheSetWithZeroTTLNoOp(t *testing.T) {
	// Defensive: if we ever pass TTL=0 by mistake, ensure we don't store
	// a forever-stale entry. The internal set() returns early on ttl<=0.
	c := NewChangeCache()
	c.set(changeCacheKey{Source: prices.SourceCoinGecko, Entity: "mvrk", Currency: "usd"},
		decimal.NewFromInt(1), time.Now(), 0)
	if c.Len() != 0 {
		t.Errorf("expected no entry after 0-TTL set, got len=%d", c.Len())
	}
}

func TestChangeCache_AuxKeyPartitions(t *testing.T) {
	c := NewChangeCache()
	src, ent, cur := prices.SourceEquiteez, "42", "usdt"
	bid := decimal.RequireFromString("55")
	ask := decimal.RequireFromString("57")
	c.SetNow(src, ent, "bid", cur, bid, time.Now())
	c.SetNow(src, ent, "ask", cur, ask, time.Now())

	if got, _, ok := c.GetNow(src, ent, "bid", cur); !ok || !got.Equal(bid) {
		t.Errorf("bid slot = %v (ok=%v), want 55", got, ok)
	}
	if got, _, ok := c.GetNow(src, ent, "ask", cur); !ok || !got.Equal(ask) {
		t.Errorf("ask slot = %v (ok=%v), want 57", got, ok)
	}
	if _, _, ok := c.GetNow(src, ent, "last", cur); ok {
		t.Error("last slot must be a miss — sides must not cross-contaminate")
	}
}
