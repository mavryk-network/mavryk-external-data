package primary

import (
	"context"
	"testing"
	"time"

	app "quotes/internal/core/application/prices"
)

func TestHoldings_UsesFinalConditionsOfTheBlock(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	launch := launchHistory{launchpad: "launchpad", key: "launch", payments: knownPayments(testPayment), updates: []update{
		{ID: 1, Level: 10, Timestamp: base, Value: record("100000000", testPayment)},
		{ID: 2, Level: 30, Timestamp: base.Add(20 * time.Second), Value: record("120000000", testPayment)},
		{ID: 3, Level: 30, Timestamp: base.Add(20 * time.Second), Value: record("140000000", testPayment)},
	}}
	h := &History{}
	covered := head{Level: 100, Timestamp: base.Add(time.Hour)}
	for _, tc := range []struct {
		level       int64
		at          time.Time
		mode, price string
		from        int64
	}{
		{10, base, app.PrimaryHoldings, "100", 10},
		{21, base.Add(11 * time.Second), app.PrimaryHoldings, "100", 10},
		{30, base.Add(20 * time.Second), app.PrimaryHoldings, "140", 30},
		{30, base.Add(25 * time.Second), app.PrimaryHoldings, "140", 30},
		{31, base.Add(30 * time.Second), app.PrimaryTransfer, "140", 30},
	} {
		req := app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: tc.level, At: tc.at, Mode: tc.mode}
		terms, matches, err := h.at(launch, req, covered)
		if err != nil || !matches || terms.Price != tc.price || terms.ValidFromLevel != tc.from {
			t.Fatalf("%+v: terms=%+v, matches=%v, err=%v", req, terms, matches, err)
		}
	}
	for _, mode := range []string{"", app.PrimaryTransfer} {
		_, _, err := h.at(launch, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: 30, At: base.Add(20 * time.Second), Mode: mode}, covered)
		if err == nil || err.Error() != "primary valuation: same_block_change" {
			t.Fatalf("transfer mode %q inherited end-block conditions: %v", mode, err)
		}
	}
	_, _, err := h.at(launch, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: 30, At: base.Add(19 * time.Second), Mode: app.PrimaryHoldings}, covered)
	if err == nil {
		t.Fatal("future block conditions were accepted")
	}
}

func TestHoldings_UsesFinalPaymentMetadataAndRejectsDeletion(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	launch := launchHistory{launchpad: "launchpad", key: "launch", updates: []update{{ID: 1, Level: 10, Timestamp: base, Value: record("100000000", testPayment)}}, payments: map[paymentIdentity]metadataHistory{
		{Contract: testPayment, TokenID: "0"}: {
			{Level: 1, Timestamp: base.Add(-time.Hour), Scale: primaryScale(6)},
			{Level: 30, Timestamp: base.Add(20 * time.Second), Scale: primaryScale(7)},
			{Level: 30, Timestamp: base.Add(20 * time.Second), Scale: primaryScale(8)},
			{Level: 40, Timestamp: base.Add(30 * time.Second)},
		},
	}}
	h := &History{}
	covered := head{Level: 100, Timestamp: base.Add(time.Hour)}
	req := app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: 30, At: base.Add(20 * time.Second), Mode: app.PrimaryHoldings}
	terms, _, err := h.at(launch, req, covered)
	if err != nil || terms.Price != "1" || terms.PaymentDecimals != 8 || terms.ValidFromLevel != 30 {
		t.Fatalf("final scale: terms=%+v err=%v", terms, err)
	}
	req.Mode = ""
	if _, _, err := h.at(launch, req, covered); err == nil {
		t.Fatal("transfer inherited ambiguous metadata")
	}
	req.Mode, req.Level, req.At = app.PrimaryHoldings, 40, base.Add(30*time.Second)
	if _, _, err := h.at(launch, req, covered); err == nil {
		t.Fatal("deleted metadata became a price")
	}
	req.Network, req.Level, req.At = "basenet", 101, covered.Timestamp
	h.snapshot, h.expires = &snapshot{head: covered, histories: []launchHistory{launch}}, time.Now().Add(time.Hour)
	if _, err := h.Terms(context.Background(), req); err == nil {
		t.Fatal("uncovered block was priced")
	}
}
