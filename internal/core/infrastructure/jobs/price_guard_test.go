package jobs

import (
	"encoding/json"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/shopspring/decimal"
)

func flexFromJSON(t *testing.T, raw string) equiteez.FlexibleFloat {
	t.Helper()
	var f equiteez.FlexibleFloat
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return f
}

func TestMappablePrice(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		shift      int32
		wantOK     bool
		wantReason string
		wantPrice  string
	}{
		{"normal", `"56250000"`, -6, true, "", "56.25"},
		{"no shift", `"100"`, 0, true, "", "100"},
		{"zero skipped quietly", `"0"`, -6, false, "", ""},
		{"negative skipped quietly", `-5`, -6, false, "", ""},
		{"null skipped quietly", `null`, -6, false, "", ""},
		{"NaN dropped and counted", `"NaN"`, -6, false, dropReasonNonFinite, ""},
		{"Inf dropped and counted", `"Inf"`, -6, false, dropReasonNonFinite, ""},
		{"unstorable magnitude dropped", `"1e40"`, 0, false, dropReasonOutOfRange, ""},
		{"huge exponent dropped fast", `"1e1000000000"`, 0, false, dropReasonOutOfRange, ""},
		{"shift brings it in range", `"1e25"`, -18, true, "", "10000000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			price, reason, ok := mappablePrice(flexFromJSON(t, c.raw), c.shift)
			if ok != c.wantOK || reason != c.wantReason {
				t.Fatalf("ok=%v reason=%q, want ok=%v reason=%q", ok, reason, c.wantOK, c.wantReason)
			}
			if c.wantOK && price.String() != c.wantPrice {
				t.Errorf("price = %s, want %s", price, c.wantPrice)
			}
		})
	}
}

// TestMappablePrice_HugeExponentIsCheap pins the DoS guard: deciding on
// metadata must not rescale the value into a 10^n big.Int.
func TestMappablePrice_HugeExponentIsCheap(t *testing.T) {
	v := flexFromJSON(t, `"1e2000000000"`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, reason, ok := mappablePrice(v, 0); ok || reason != dropReasonOutOfRange {
			t.Errorf("ok=%v reason=%q, want out_of_range", ok, reason)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("mappablePrice did not return within 2s — magnitude check is rescaling")
	}
}

// TestOrderbookToPoints_NonFiniteSkipsOnlyThatSide proves the blast radius of
// one poisoned field is a single side, not the whole tick.
func TestOrderbookToPoints_NonFiniteSkipsOnlyThatSide(t *testing.T) {
	pair := prices.RWAPair{ID: 7, Source: prices.SourceEquiteez, QuoteSymbol: "USDT"}
	raw := `{"highest_buy_price":"NaN","lowest_sell_price":"57000000","last_matched_price":"56250000"}`
	var ob equiteez.EquiteezOrderbook
	if err := json.Unmarshal([]byte(raw), &ob); err != nil {
		t.Fatalf("a non-finite field must not fail the decode: %v", err)
	}

	pts := orderbookToPoints(pair, &ob, 6, time.Now().UTC())
	if len(pts) != 2 {
		t.Fatalf("len(pts) = %d, want 2 (ask + last survive)", len(pts))
	}
	for _, p := range pts {
		if p.Metric == string(prices.SideBid) {
			t.Error("bid was built from a NaN")
		}
	}
	if !pts[1].Price.Equal(decimal.RequireFromString("56.25")) {
		t.Errorf("last = %s, want 56.25", pts[1].Price)
	}
}

// TestOrdersToLastPoints_NonFiniteRowSkippedBatchSurvives: the poisoned row
// drops out and the rest of the batch still maps, so the cursor advances.
func TestOrdersToLastPoints_NonFiniteRowSkippedBatchSurvives(t *testing.T) {
	pair := prices.RWAPair{ID: 9, Source: prices.SourceEquiteez, QuoteSymbol: "USDT"}
	raw := `[
		{"id":1,"price_per_rwa_token":"NaN","ended_at":"2025-08-08T14:42:10Z"},
		{"id":2,"price_per_rwa_token":"56250000","ended_at":"2025-08-08T15:00:00Z"}
	]`
	var orders []equiteez.OrderbookOrder
	if err := json.Unmarshal([]byte(raw), &orders); err != nil {
		t.Fatalf("a non-finite field must not fail the batch decode: %v", err)
	}

	pts := ordersToLastPoints(pair, orders, 6)
	if len(pts) != 1 {
		t.Fatalf("len(pts) = %d, want 1 (only the healthy order)", len(pts))
	}
	if !pts[0].Price.Equal(decimal.RequireFromString("56.25")) {
		t.Errorf("price = %s, want 56.25", pts[0].Price)
	}
	// The cursor reads raw orders, so it steps past the poisoned row too.
	if cur, ok := advanceOrderCursor(orders); !ok || cur.ID != 2 {
		t.Errorf("cursor = %+v (ok=%v), want id=2", cur, ok)
	}
}

// A tiny exponent is as dangerous as a huge one: pgx renders numeric
// digit-by-digit, so an unbounded negative exponent burns quadratic time in
// the encoder before Postgres sees the statement.
func TestMappablePrice_TinyExponentRejectedCheaply(t *testing.T) {
	for _, raw := range []string{`"1e-1000000000"`, `1e-500000`, `"0.1e-999999999"`} {
		v := flexFromJSON(t, raw)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, reason, ok := mappablePrice(v, -6); ok || reason != dropReasonOutOfRange {
				t.Errorf("%s: ok=%v reason=%q, want out_of_range", raw, ok, reason)
			}
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: mappablePrice did not return within 2s", raw)
		}
	}
}

// The column rounds to scale 18, and that rounding can carry into a 21st
// integer digit — which a check on the unrounded value would admit.
func TestMappablePrice_RoundingCarryAtTheBoundary(t *testing.T) {
	cases := []struct {
		raw    string
		wantOK bool
	}{
		{`"99999999999999999999.999999999999999999"`, true},   // exactly representable
		{`"99999999999999999999.9999999999999999995"`, false}, // rounds to 10^20
		{`"9999999999999999999.9999999999999999995"`, true},   // carries to 10^19, still fits
		{`"0.000000000000000000000000000000000001"`, false},   // below the column scale
	}
	for _, c := range cases {
		_, reason, ok := mappablePrice(flexFromJSON(t, c.raw), 0)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v reason=%q, want ok=%v", c.raw, ok, reason, c.wantOK)
		}
	}
}

// The drop counter is the only signal that a value was discarded rather than
// stored, so assert it actually moves — otherwise both increment sites could
// be deleted with the suite still green.
func TestOrderbookToPoints_CountsTheDrop(t *testing.T) {
	pair := prices.RWAPair{ID: 11, Source: prices.SourceEquiteez, QuoteSymbol: "USDT"}
	counter := metrics.IngestRowsDroppedTotal.WithLabelValues(
		string(prices.SourceEquiteez), "11", dropReasonNonFinite)
	before := counterValue(t, counter)

	var ob equiteez.EquiteezOrderbook
	if err := json.Unmarshal([]byte(`{"highest_buy_price":"NaN","last_matched_price":"56250000"}`), &ob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	orderbookToPoints(pair, &ob, 6, time.Now().UTC())

	if got := counterValue(t, counter) - before; got != 1 {
		t.Errorf("ingest_rows_dropped_total delta = %v, want 1", got)
	}
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}
