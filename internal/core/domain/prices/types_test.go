package prices

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestNewCurrency(t *testing.T) {
	cases := []struct {
		in    string
		want  Currency
		isErr bool
	}{
		{"usd", CurrencyUSD, false},
		{"  EUR ", CurrencyEUR, false},
		{"BTC", CurrencyBTC, false},
		{"xyz", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := NewCurrency(c.in)
		if (err != nil) != c.isErr {
			t.Errorf("NewCurrency(%q): err=%v want isErr=%v", c.in, err, c.isErr)
			continue
		}
		if got != c.want {
			t.Errorf("NewCurrency(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewSide(t *testing.T) {
	for _, in := range []string{"bid", "ASK", "  last", "MID"} {
		if _, err := NewSide(in); err != nil {
			t.Errorf("NewSide(%q) returned err: %v", in, err)
		}
	}
	for _, in := range []string{"buy", "", "sell"} {
		if _, err := NewSide(in); err == nil {
			t.Errorf("NewSide(%q): want err, got nil", in)
		}
	}
}

func TestNewToken_RegistryDriven(t *testing.T) {
	original := tokenRegistry
	defer func() { tokenRegistry = original }()

	RegisterTokens([]TokenInfo{
		{Symbol: "mvrk", Name: "Mavryk", Enabled: true},
		{Symbol: "abc", Name: "ABC", Enabled: false},
	})

	if _, err := NewToken("MVRK"); err != nil {
		t.Errorf("NewToken('MVRK'): unexpected err %v", err)
	}
	if _, err := NewToken("nope"); err == nil {
		t.Errorf("NewToken('nope'): want err, got nil")
	}
	enabled := EnabledTokens()
	if len(enabled) != 1 || enabled[0].Symbol != "mvrk" {
		t.Errorf("EnabledTokens: got %+v, want only mvrk", enabled)
	}
}

func TestLatestSnapshot(t *testing.T) {
	t1 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t2.Add(time.Minute)

	points := []PricePoint{
		{Source: SourceCoinGecko, EntityKey: "mvrk", Timestamp: t1, Metric: "usd", Price: decimal.NewFromFloat(1.0)},
		{Source: SourceCoinGecko, EntityKey: "mvrk", Timestamp: t2, Metric: "usd", Price: decimal.NewFromFloat(1.1)},
		{Source: SourceCoinGecko, EntityKey: "mvrk", Timestamp: t3, Metric: "eur", Price: decimal.NewFromFloat(0.95)},
	}
	snap, ok := LatestSnapshot(points)
	if !ok {
		t.Fatal("LatestSnapshot returned ok=false on non-empty input")
	}
	if !snap.Timestamp.Equal(t3) {
		t.Errorf("snapshot timestamp = %v, want %v (newest)", snap.Timestamp, t3)
	}
	if got := snap.Values["usd"].String(); got != "1.1" {
		t.Errorf("values[usd] = %q, want 1.1", got)
	}
	if got := snap.Values["eur"].String(); got != "0.95" {
		t.Errorf("values[eur] = %q, want 0.95", got)
	}
}

func TestLatestSnapshot_Empty(t *testing.T) {
	if _, ok := LatestSnapshot(nil); ok {
		t.Error("LatestSnapshot(nil) returned ok=true, want false")
	}
}

func TestQuery_IsLatest(t *testing.T) {
	if !(Query{}).IsLatest() {
		t.Error("zero Query should be IsLatest()")
	}
	if (Query{From: time.Now()}).IsLatest() {
		t.Error("Query with From set must not be IsLatest()")
	}
}
