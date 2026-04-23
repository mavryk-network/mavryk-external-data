package equiteez

import (
	"encoding/json"
	"testing"
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

func TestNormalizedUSDPerTokenFromOrderbook(t *testing.T) {
	ob := &EquiteezOrderbook{
		LastMatchedPrice: FlexibleFloat(1_500_000),
		Currencies: []OrderbookCurrency{
			{CurrencyName: "USDT"},
		},
	}
	usd, ok := NormalizedUSDPerTokenFromOrderbook(ob)
	if !ok || usd != 1.5 {
		t.Fatalf("got %v %v", usd, ok)
	}
}
