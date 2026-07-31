package equiteez

import "testing"

// TestBaseTierPrice pins the winning-payment contract: the base tier is the
// MAX price across every option's payments, and the currency label AND quote
// token address must come from that same payment row — the schema allows
// several payment tokens per option, and mixing rows would pair the price
// with the wrong settlement token.
func TestBaseTierPrice(t *testing.T) {
	usdt := &LaunchTokenRef{Address: "KT1UsdtPay"}
	eurl := &LaunchTokenRef{Address: "KT1EurlPay"}

	row := LaunchRow{SaleOptions: []LaunchSaleOptionRow{
		{Name: "Pinnacle", Payments: []LaunchPaymentRow{
			{Name: "EURL", Price: FlexBig("75000000"), Token: eurl},
		}},
		{Name: "Starter", Payments: []LaunchPaymentRow{
			{Name: "USDT", Price: FlexBig("100000000"), Token: usdt},
		}},
	}}

	raw, currency, quoteAddr, ok := row.BaseTierPrice()
	if !ok {
		t.Fatal("ok = false, want a usable price")
	}
	if raw != "100000000" || currency != "USDT" {
		t.Errorf("base tier = %s %s, want 100000000 USDT (the max)", raw, currency)
	}
	if quoteAddr != "KT1UsdtPay" {
		t.Errorf("quoteAddr = %q, want KT1UsdtPay — must travel with the WINNING payment", quoteAddr)
	}
}

// A winning payment without a nested token ref degrades to "", never panics —
// the repository preserves the previously stored address in that case.
func TestBaseTierPrice_NoTokenRef(t *testing.T) {
	row := LaunchRow{SaleOptions: []LaunchSaleOptionRow{
		{Payments: []LaunchPaymentRow{{Name: "USDT", Price: FlexBig("100")}}},
	}}
	_, _, quoteAddr, ok := row.BaseTierPrice()
	if !ok || quoteAddr != "" {
		t.Errorf("got (%v, %q), want (true, \"\")", ok, quoteAddr)
	}

	// No usable price at all → ok=false.
	if _, _, _, ok := (LaunchRow{}).BaseTierPrice(); ok {
		t.Error("empty launch: ok = true, want false")
	}
}
