package primary

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quotes/internal/config"
	app "quotes/internal/core/application/prices"
)

const testAssetContract = "KT1HR56nVuVDnidsabd2TBB9YvAtKbCbHKpP"
const testPayment = "KT1Pn5Zpx1bJx5H51btk92pfwvUMCKtp2Q2v"

func record(price, currency string) *launchRecord {
	var r launchRecord
	b := `{"tokenContractAddress":"` + testAssetContract + `","tokenId":"0","status":"ACTIVE","saleOptions":{"Starter":{"payments":{"USDT":{"price":"` + price + `","currency":{"fa2":{"tokenContractAddress":"` + currency + `","tokenId":"0"}}}}}}}`
	if err := json.Unmarshal([]byte(b), &r); err != nil {
		panic(err)
	}
	return &r
}

func TestHistoricalStarter_PriceAndPaymentIntervals(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	oldPayment := "KT19bKTs9qsoBrspRNwsHn46YarEWuj3Vjc6"
	old := record("100000000", oldPayment)
	newPrice := record("120000000", testPayment)
	history := launchHistory{launchpad: "launchpad", key: "ANTH", updates: []update{
		{ID: 1, Level: 10, Timestamp: base, Action: "add_key", Value: old},
		{ID: 2, Level: 20, Timestamp: base.Add(10 * time.Second), Action: "update_key", Value: record("100000000", oldPayment)},
		{ID: 3, Level: 30, Timestamp: base.Add(20 * time.Second), Action: "update_key", Value: newPrice},
	}}
	history.payments = knownPayments(oldPayment, testPayment)
	h := &History{}
	covered := head{Level: 100, Timestamp: base.Add(time.Hour)}
	for _, tc := range []struct {
		level          int64
		sec            int
		price, payment string
		from           int64
	}{
		{21, 11, "100", oldPayment, 10}, {31, 21, "120", testPayment, 30},
	} {
		got, matches, err := h.at(history, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: tc.level, At: base.Add(time.Duration(tc.sec) * time.Second)}, covered)
		if err != nil || !matches || got.Price != tc.price || got.PaymentContract != tc.payment || got.ValidFromLevel != tc.from {
			t.Fatalf("got %+v, matches=%v err=%v", got, matches, err)
		}
		if tc.level == 21 && (got.ValidUntilLevel == nil || *got.ValidUntilLevel != 30) {
			t.Fatal("old price must stop at its actual change")
		}
	}
	// Conditions changed within this block; operation order cannot be proved
	// by timestamp or block alone. A quantity-only update is safe.
	_, _, err := h.at(history, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: 30, At: base.Add(20 * time.Second)}, covered)
	if err == nil || err.Error() != "primary valuation: same_block_change" {
		t.Fatalf("changed block: %v", err)
	}
	_, matches, err := h.at(history, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: 20, At: base.Add(10 * time.Second)}, covered)
	if err != nil || !matches {
		t.Fatalf("purchase-only update must keep the known price: %v", err)
	}
}

func TestHistoricalStarter_RefusesUnconfirmedInputs(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{"unknown_payment", "multiple_options", "paused", "wrong_id", "closed", "invalid_price"} {
		t.Run(name, func(t *testing.T) {
			r := record("100000000", testPayment)
			payments := knownPayments(testPayment)
			req := app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: 20, At: base.Add(time.Minute)}
			switch name {
			case "unknown_payment":
				payments = nil
			case "multiple_options":
				r.Options["Pinnacle"] = r.Options["Starter"]
			case "paused":
				r.IsPaused = true
			case "wrong_id":
				req.TokenID = "1"
			case "closed":
				r.SaleClosed = &base
			case "invalid_price":
				p := r.Options["Starter"]
				payment := p.Payments["USDT"]
				payment.Price = "-1"
				p.Payments["USDT"] = payment
			}
			history := launchHistory{payments: payments, updates: []update{{ID: 1, Level: 10, Timestamp: base, Value: r}}}
			_, matches, err := (&History{}).at(history, req, head{Level: 100, Timestamp: base.Add(time.Hour)})
			if name == "wrong_id" {
				if matches {
					t.Fatal("another token ID inherited price")
				}
				return
			}
			if !matches || err == nil {
				t.Fatalf("must fail closed: matches=%v err=%v", matches, err)
			}
		})
	}
}

func TestHistory_LoadPaginatesAndCoalescesCompleteEvidence(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload any
		if discovery, found := discoveryFixture(r, base); found {
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		switch {
		case r.URL.Path == "/head":
			payload = head{Chain: "basenet", Level: 1002}
		case r.URL.Path == "/blocks/1000":
			payload = head{Level: 1000, Hash: "block", Timestamp: base.Add(2 * time.Hour)}
		case strings.HasSuffix(r.URL.Path, "/storage"):
			payload = map[string]int{"launchLedger": 12}
		case r.URL.Path == "/bigmaps/12/keys":
			payload = []map[string]string{{"hash": "exprKey", "key": "ANTH"}}
		case strings.HasSuffix(r.URL.Path, "/updates"):
			if r.URL.Query().Get("sort.asc") != "id" || r.URL.Query().Get("level.le") != "1000" {
				t.Error("history must pin its coverage and cursor order")
			}
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			page := []update{}
			for i := offset; i < offset+historyPageSize && i < 600; i++ {
				action := "update_key"
				if i == 0 {
					action = "add_key"
				}
				page = append(page, update{ID: int64(i + 1), Level: int64(i + 1), Timestamp: base.Add(time.Duration(i) * time.Second), Action: action, Value: record("725000000", testPayment)})
			}
			payload = page
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()
	h := newTestHistory(server.URL)
	for i := 0; i < 2; i++ {
		got, err := h.Terms(context.Background(), app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: 650, At: base.Add(650 * time.Second)})
		if err != nil || got.Price != "725" || got.CoverageLevel != 1000 {
			t.Fatalf("got %+v err=%v", got, err)
		}
	}
	if calls.Load() != 11 {
		t.Fatalf("expected one shared complete load, got %d HTTP calls", calls.Load())
	}
}

func TestHistoricalStarter_ObservedANTHTransfers(t *testing.T) {
	// Public launchLedger updates captured on 2026-10-06. Payment identities
	// change three times while the Starter price stays 725.
	b, err := os.ReadFile("testdata/anth_launch_updates.json")
	if err != nil {
		t.Fatal(err)
	}
	var updates []update
	if err := json.Unmarshal(b, &updates); err != nil {
		t.Fatal(err)
	}
	history := launchHistory{launchpad: "KT1U6KXwy8vduoq86HBjGp9m2Czc8rZM85MN", key: "ANTH-issuance-v1", updates: updates}
	history.payments = knownPayments(testPayment)
	h := &History{}
	coveredAt, _ := time.Parse(time.RFC3339, "2026-10-06T10:00:00Z")
	for _, tc := range []struct {
		at    string
		level int64
	}{
		{"2026-10-05T13:31:42Z", 6635052},
		{"2026-10-05T23:15:04Z", 6642015},
		{"2026-10-06T04:26:44Z", 6645755},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		terms, matches, err := h.at(history, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: tc.level, At: at}, head{Level: 6649270, Timestamp: coveredAt})
		if err != nil || !matches || terms.Price != "725" || terms.PaymentContract != testPayment || terms.ValidFromLevel != 6515339 {
			t.Fatalf("transfer at %s: terms=%+v matches=%v err=%v", tc.at, terms, matches, err)
		}
	}
}

func primaryScale(n int) *int { return &n }

func knownPayments(contracts ...string) map[paymentIdentity]metadataHistory {
	out := map[paymentIdentity]metadataHistory{}
	for _, contract := range contracts {
		out[paymentIdentity{Contract: contract, TokenID: "0"}] = metadataHistory{{Level: 1, Timestamp: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Scale: primaryScale(6)}}
	}
	return out
}

func newTestHistory(baseURL string) *History {
	h := NewHistory(config.MvKTConfig{IndexerURL: baseURL}, &config.APIConfig{OutboundHTTPCircuitBreakerDisabled: true, OutboundHTTPRetryMaxAttempts: 1})
	h.allowlistURL = baseURL + "/allowlist"
	return h
}

func discoveryFixture(r *http.Request, base time.Time) (any, bool) {
	switch r.URL.Path {
	case "/allowlist":
		return map[string]any{"schema_version": 1, "launchpads": []string{"KT1U6KXwy8vduoq86HBjGp9m2Czc8rZM85MN"}}, true
	case "/contracts/" + testPayment + "/storage":
		return map[string]int{"token_metadata": 13}, true
	case "/bigmaps/13/keys":
		return []map[string]string{{"hash": "exprMetadata", "key": "0"}}, true
	case "/bigmaps/13/keys/exprMetadata/updates":
		return []metadataUpdate{{ID: 1, Level: 1, Timestamp: base.Add(-time.Second), Action: "add_key", Value: &tokenMetadata{TokenID: "0", Info: map[string]string{"decimals": "36"}}}}, true
	}
	return nil, false
}

func TestHistory_OutageIsCoalescedAndRetriedAfterExpiry(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	h := NewHistory(config.MvKTConfig{IndexerURL: server.URL}, &config.APIConfig{OutboundHTTPCircuitBreakerDisabled: true, OutboundHTTPRetryMaxAttempts: 1})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.readSnapshot(context.Background()); err == nil {
				t.Error("outage returned historical evidence")
			}
		}()
	}
	wg.Wait()
	for i := 0; i < app.PrimaryBatchLimit; i++ {
		if _, err := h.readSnapshot(context.Background()); err == nil {
			t.Fatal("cached outage returned historical evidence")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one upstream attempt, got %d", calls.Load())
	}
	h.mu.Lock()
	h.expires = time.Now().Add(-time.Second)
	h.mu.Unlock()
	_, _ = h.readSnapshot(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("expired outage must be retried, got %d calls", calls.Load())
	}
}

func TestHistory_RemovalDoesNotKeepThePreviousPrice(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload any
		if discovery, found := discoveryFixture(r, base); found {
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		switch {
		case r.URL.Path == "/head":
			payload = head{Chain: "basenet", Level: 102}
		case r.URL.Path == "/blocks/100":
			payload = head{Level: 100, Hash: "block", Timestamp: base.Add(time.Hour)}
		case strings.HasSuffix(r.URL.Path, "/storage"):
			payload = map[string]int{"launchLedger": 12}
		case r.URL.Path == "/bigmaps/12/keys":
			payload = []map[string]string{{"hash": "exprKey", "key": "ANTH"}}
		case strings.HasSuffix(r.URL.Path, "/updates"):
			payload = []update{
				{ID: 1, Level: 10, Timestamp: base, Action: "add_key", Value: record("725000000", testPayment)},
				{ID: 2, Level: 20, Timestamp: base.Add(time.Minute), Action: "remove_key", Value: record("725000000", testPayment)},
			}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()
	h := newTestHistory(server.URL)
	req := app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: 15, At: base.Add(30 * time.Second)}
	if got, err := h.Terms(context.Background(), req); err != nil || got.Price != "725" {
		t.Fatalf("valid history before removal: got %+v err=%v", got, err)
	}
	req.Level, req.At = 30, base.Add(2*time.Minute)
	if _, err := h.Terms(context.Background(), req); err == nil {
		t.Fatal("removed launch retained its former price")
	}
}
