package primary

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	app "quotes/internal/core/application/prices"
)

func TestMetadataScale_RequiresExplicitHexDecimalsForExactToken(t *testing.T) {
	for _, tc := range []struct {
		name, id, raw string
		want          int
		valid         bool
	}{
		{"six", "0", "36", 6, true}, {"zero", "0", "30", 0, true}, {"forty", "0", "3430", 40, true},
		{"missing", "0", "", 0, false}, {"malformed", "0", "zz", 0, false}, {"not_ascii", "0", "06", 0, false},
		{"negative", "0", "2d31", 0, false}, {"excessive", "0", "3431", 0, false}, {"wrong_id", "1", "36", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := metadataScale(&tokenMetadata{TokenID: tc.id, Info: map[string]string{"decimals": tc.raw}}, "0")
			if tc.valid && (got == nil || *got != tc.want) || !tc.valid && got != nil {
				t.Fatalf("got %v, expected valid=%v scale=%d", got, tc.valid, tc.want)
			}
		})
	}
	if metadataScale(nil, "0") != nil {
		t.Fatal("null metadata inherited decimals")
	}
}

func TestHistoricalStarter_MetadataChangesConstrainThePriceInterval(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	launch := launchHistory{launchpad: "launchpad", key: "ANTH", updates: []update{{ID: 1, Level: 10, Timestamp: base, Value: record("100000000", testPayment)}}, payments: map[paymentIdentity]metadataHistory{
		{Contract: testPayment, TokenID: "0"}: {
			{Level: 1, Timestamp: base.Add(-time.Hour), Scale: primaryScale(6)},
			{Level: 30, Timestamp: base.Add(20 * time.Second), Scale: primaryScale(8)},
			{Level: 40, Timestamp: base.Add(30 * time.Second)}, // missing/deleted metadata
		},
	}}
	h := &History{}
	covered := head{Level: 100, Timestamp: base.Add(time.Hour)}
	for _, tc := range []struct {
		level, sec int64
		price      string
		from       int64
		until      int64
	}{
		{21, 11, "100", 10, 30}, {31, 21, "1", 30, 40},
	} {
		got, matches, err := h.at(launch, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: tc.level, At: base.Add(time.Duration(tc.sec) * time.Second)}, covered)
		if err != nil || !matches || got.Price != tc.price || got.ValidFromLevel != tc.from || got.ValidUntilLevel == nil || *got.ValidUntilLevel != tc.until {
			t.Fatalf("level %d: got %+v err=%v", tc.level, got, err)
		}
	}
	for _, level := range []int64{30, 41} {
		_, _, err := h.at(launch, app.PrimaryRequest{Contract: testAssetContract, TokenID: "0", Level: level, At: base.Add(time.Duration(level-10) * time.Second)}, covered)
		if err == nil {
			t.Fatalf("unconfirmed scale at level %d was priced", level)
		}
	}
}

func TestMetadataReader_DeletedMalformedOrIncompleteHistoryIsUnknown(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{"ok", "zero", "deleted", "malformed", "wrong_id", "missing_birth", "missing_bigmap", "wrong_key"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				switch r.URL.Path {
				case "/contracts/" + testPayment + "/storage":
					ptr := 13
					if name == "missing_bigmap" {
						ptr = 0
					}
					body = map[string]int{"token_metadata": ptr}
				case "/bigmaps/13/keys":
					if r.URL.Query().Get("key") != "0" {
						t.Error("token identity not passed to metadata lookup")
					}
					key := "0"
					if name == "wrong_key" {
						key = "1"
					}
					body = []map[string]string{{"hash": "exprMetadata", "key": key}}
				case "/bigmaps/13/keys/exprMetadata/updates":
					if r.URL.Query().Get("level.le") != "100" {
						t.Error("metadata history was not pinned")
					}
					id, raw, action := "0", "36", "add_key"
					if name == "wrong_id" {
						id = "1"
					}
					if name == "malformed" {
						raw = "bad"
					}
					if name == "zero" {
						raw = "30"
					}
					if name == "missing_birth" {
						action = "update_key"
					}
					page := []metadataUpdate{{ID: 1, Level: 1, Timestamp: base, Action: action, Value: &tokenMetadata{TokenID: id, Info: map[string]string{"decimals": raw}}}}
					if name == "deleted" {
						page = append(page, metadataUpdate{ID: 2, Level: 10, Timestamp: base.Add(time.Second), Action: "remove_key", Value: page[0].Value})
					}
					body = page
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			rows := 0
			versions, err := newTestHistory(server.URL).loadMetadata(context.Background(), paymentIdentity{Contract: testPayment, TokenID: "0"}, 100, &rows)
			if err == nil {
				_, err = versions.at(app.PrimaryRequest{Level: 20, At: base.Add(time.Minute)})
			}
			if (name == "ok" || name == "zero") != (err == nil) {
				t.Fatalf("unexpected metadata availability: %v", err)
			}
		})
	}
}
