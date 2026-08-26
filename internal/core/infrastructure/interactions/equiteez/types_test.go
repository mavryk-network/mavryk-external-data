package equiteez

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFlexibleFloat_UnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		raw string
		exp string
	}{
		{`42.5`, "42.5"},
		{`"1000000"`, "1000000"},
		{`null`, "0"},
		// Exceeds float64's 53-bit exact-integer range — must survive intact.
		{`"123456789012345678901234567890"`, "123456789012345678901234567890"},
	} {
		var f FlexibleFloat
		if err := json.Unmarshal([]byte(tc.raw), &f); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if f.Decimal().String() != tc.exp {
			t.Fatalf("%s: got %v want %v", tc.raw, f.Decimal(), tc.exp)
		}
	}
}

func TestFlexibleFloat_RejectsNonFinite(t *testing.T) {
	for _, raw := range []string{`"NaN"`, `"Inf"`, `"+Inf"`, `"-Infinity"`} {
		var f FlexibleFloat
		if err := json.Unmarshal([]byte(raw), &f); err == nil {
			t.Errorf("%s: expected error, parsed as %v", raw, f.Decimal())
		}
	}
}

// Hasura emits bigint columns as quoted strings and the GraphQL Int scalar as
// bare numbers; mixed shapes within one row are normal. This guards the
// canonical orderbook_order payload shape.
func TestOrderbookOrder_UnmarshalJSON(t *testing.T) {
	raw := `{
		"id": 43,
		"order_type": 1,
		"price_per_rwa_token": "55000000",
		"fulfilled_amount": "2000000",
		"ended_at": "2025-08-08T14:42:10+00:00",
		"operation_hash": "ooXYZ"
	}`
	var o OrderbookOrder
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if o.ID != 43 {
		t.Errorf("id = %d, want 43", o.ID)
	}
	if o.OrderType != 1 {
		t.Errorf("order_type = %d, want 1", o.OrderType)
	}
	if o.PricePerRWAToken.Decimal().String() != "55000000" {
		t.Errorf("price = %v, want 55000000", o.PricePerRWAToken.Decimal())
	}
	if o.FulfilledAmount.Decimal().String() != "2000000" {
		t.Errorf("fulfilled_amount = %v, want 2000000", o.FulfilledAmount.Decimal())
	}
	if _, err := time.Parse(time.RFC3339, o.EndedAt); err != nil {
		t.Errorf("ended_at not RFC3339: %v", err)
	}
}

// TestOrderbookQuoteAccessors — QuoteSymbol / QuoteTokenAddress must be
// nil-safe at every level: nil receiver, no currency rows, currency row
// without a nested token. The sync loop calls them on whatever the indexer
// returned, so a partial payload must degrade to "", never panic.
func TestOrderbookQuoteAccessors(t *testing.T) {
	var nilOB *EquiteezOrderbook
	if got := nilOB.QuoteTokenAddress(); got != "" {
		t.Errorf("nil receiver: %q, want empty", got)
	}
	if got := (&EquiteezOrderbook{}).QuoteTokenAddress(); got != "" {
		t.Errorf("no currencies: %q, want empty", got)
	}
	noToken := &EquiteezOrderbook{Currencies: []OrderbookCurrency{{CurrencyName: "USDT"}}}
	if got := noToken.QuoteTokenAddress(); got != "" {
		t.Errorf("currency without token: %q, want empty", got)
	}
	if got := noToken.QuoteSymbol(); got != "USDT" {
		t.Errorf("symbol should still resolve without token: %q", got)
	}
	full := &EquiteezOrderbook{Currencies: []OrderbookCurrency{{
		CurrencyName: "USDT",
		Token:        &TokenQuoteToken{Address: "KT1VAymQuote"},
	}}}
	if got := full.QuoteTokenAddress(); got != "KT1VAymQuote" {
		t.Errorf("address = %q, want KT1VAymQuote", got)
	}
}
