package primary

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "quotes/internal/core/application/prices"
)

func TestHistory_PublicFixturesDiscoverOldPaymentsWithoutManualLists(t *testing.T) {
	var launches []update
	var payments map[string][]metadataUpdate
	b, err := os.ReadFile("testdata/anth_launch_updates.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &launches); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("testdata/usdt_metadata_updates.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &payments); err != nil {
		t.Fatal(err)
	}
	launchpad := "KT1U6KXwy8vduoq86HBjGp9m2Czc8rZM85MN"
	var allowlist atomic.Value
	allowlist.Store([]string{launchpad, launchpad})
	ptrs := map[string]int{testPayment: 3864, "KT19bKTs9qsoBrspRNwsHn46YarEWuj3Vjc6": 1181, "KT1J8yjtFGY6fiqjSNLpUjDnX2bj2HsUizAi": 3514}
	coveredAt, _ := time.Parse(time.RFC3339, "2026-10-06T12:00:00Z")
	metadataRoutes := map[string]any{}
	for contract, rows := range payments {
		path := "/bigmaps/" + strconv.Itoa(ptrs[contract]) + "/keys"
		metadataRoutes[path] = []map[string]string{{"hash": "exprMetadata", "key": "0"}}
		metadataRoutes[path+"/exprMetadata/updates"] = rows
	}
	var calls atomic.Int64
	var missingContract atomic.Value
	missingContract.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body any
		if value, found := metadataRoutes[r.URL.Path]; found {
			_ = json.NewEncoder(w).Encode(value)
			return
		}
		switch {
		case r.URL.Path == "/allowlist":
			// Only the current quote token is allowlisted. Past payment
			// identities must still come from their historical launch versions.
			body = map[string]any{"schema_version": 1, "launchpads": allowlist.Load(), "quote_tokens": []string{testPayment}}
		case r.URL.Path == "/head":
			body = head{Chain: "basenet", Level: 6651002}
		case r.URL.Path == "/blocks/6651000":
			body = head{Level: 6651000, Hash: "block", Timestamp: coveredAt}
		case strings.HasSuffix(r.URL.Path, "/storage"):
			contract := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/contracts/"), "/storage")
			if contract == missingContract.Load().(string) {
				http.NotFound(w, r)
				return
			}
			if ptr, found := ptrs[contract]; found {
				body = map[string]int{"token_metadata": ptr}
			} else {
				body = map[string]int{"launchLedger": 3896}
			}
		case r.URL.Path == "/bigmaps/3896/keys":
			body = []map[string]string{{"hash": "exprLaunch", "key": "ANTH-issuance-v1"}}
		case r.URL.Path == "/bigmaps/3896/keys/exprLaunch/updates":
			body = launches
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	h := newTestHistory(server.URL)
	for _, tc := range []struct {
		at, payment string
		level       int64
	}{
		{"2026-09-28T08:10:40Z", "KT19bKTs9qsoBrspRNwsHn46YarEWuj3Vjc6", 6510316},
		{"2026-09-28T12:39:55Z", "KT1J8yjtFGY6fiqjSNLpUjDnX2bj2HsUizAi", 6513547},
		{"2026-10-05T13:31:42Z", testPayment, 6635052},
		{"2026-10-05T23:15:04Z", testPayment, 6642015},
		{"2026-10-06T04:26:44Z", testPayment, 6645755},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		got, err := h.Terms(context.Background(), app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: tc.level, At: at})
		if err != nil || got.Price != "725" || got.PaymentContract != tc.payment || got.PaymentDecimals != 6 || got.Launchpad != launchpad || got.Source != "mvkt_launch_ledger:onchain_token_metadata" {
			t.Fatalf("historical payment at %s: got %+v err=%v", tc.at, got, err)
		}
	}
	initialCalls := calls.Load()
	newLaunchpad := "KT1UqpQV34HZ2iGBxHtPKtdriwenu83Zy7mS"
	allowlist.Store([]string{newLaunchpad})
	h.mu.Lock()
	h.expires = time.Now().Add(-time.Second)
	h.mu.Unlock()
	at, _ := time.Parse(time.RFC3339, "2026-10-05T13:31:42Z")
	got, err := h.Terms(context.Background(), app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: 6635052, At: at})
	if err != nil || got.Launchpad != newLaunchpad || calls.Load() <= initialCalls {
		t.Fatalf("allowlist refresh did not discover the replacement launchpad: %+v err=%v", got, err)
	}
	// Missing metadata on an obsolete generation must not hide the current one.
	missingContract.Store("KT19bKTs9qsoBrspRNwsHn46YarEWuj3Vjc6")
	h.mu.Lock()
	h.expires = time.Now().Add(-time.Second)
	h.mu.Unlock()
	got, err = h.Terms(context.Background(), app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: 6635052, At: at})
	if err != nil || got.Price != "725" {
		t.Fatalf("missing old metadata hid current price: %+v err=%v", got, err)
	}
	oldAt, _ := time.Parse(time.RFC3339, "2026-09-28T08:10:40Z")
	_, err = h.Terms(context.Background(), app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: 6510316, At: oldAt})
	if err == nil || err.Error() != "primary valuation: payment_metadata_missing" {
		t.Fatalf("unknown old metadata inherited a new scale: %v", err)
	}
}

func TestHistory_AllowlistUnavailableDoesNotKeepPreviousDiscovery(t *testing.T) {
	for _, response := range []string{`{"schema_version":2,"launchpads":[]}`, `{"schema_version":1}`, `{"schema_version":1,"launchpads":["KT1/bad"]}`, `not-json`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(response))
		}))
		h := newTestHistory(server.URL)
		_, err := h.launchpads(context.Background())
		server.Close()
		if err == nil {
			t.Fatalf("unconfirmed discovery accepted: %s", response)
		}
	}
}
