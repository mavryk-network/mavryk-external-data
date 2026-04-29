package equiteez

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFlexibleFloat_UnmarshalJSON(t *testing.T) {
	for _, tc := range []struct {
		raw string
		exp float64
	}{
		{`42.5`, 42.5},
		{`"1000000"`, 1e6},
		{`null`, 0},
	} {
		var f FlexibleFloat
		if err := json.Unmarshal([]byte(tc.raw), &f); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if f.Float64() != tc.exp {
			t.Fatalf("%s: got %v want %v", tc.raw, f.Float64(), tc.exp)
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
	if o.PricePerRWAToken.Float64() != 55_000_000 {
		t.Errorf("price = %v, want 55_000_000", o.PricePerRWAToken.Float64())
	}
	if o.FulfilledAmount.Float64() != 2_000_000 {
		t.Errorf("fulfilled_amount = %v, want 2_000_000", o.FulfilledAmount.Float64())
	}
	if _, err := time.Parse(time.RFC3339, o.EndedAt); err != nil {
		t.Errorf("ended_at not RFC3339: %v", err)
	}
}
