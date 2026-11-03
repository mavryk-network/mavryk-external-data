package mvkt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	rwa "quotes/internal/core/domain/valuation"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplyHistoricalTotalUsesStoragePointerAtLevel(t *testing.T) {
	for _, tc := range []struct{ name, storage, rows, want, reason string }{
		{"exact", `{"total_supply":42}`, `[{"id":1,"active":true,"key":"7","value":"900719925474099300000000"}]`, "900719925474099300000000", ""},
		{"zero", `{"total_supply":42}`, `[{"id":1,"active":true,"key":"7","value":"0"}]`, "0", ""},
		{"killed", `{"total_supply":43,"isKilled":true}`, "", "0", ""},
		{"unsupported", `{}`, "", "", "source_contract_unsupported"},
		{"missing", `{"total_supply":42}`, `[]`, "", "invalid_response"},
		{"null", `{"total_supply":42}`, `null`, "", "invalid_response"},
		{"wrong token", `{"total_supply":42}`, `[{"id":1,"active":true,"key":"8","value":"10"}]`, "", "invalid_response"},
		{"inactive", `{"total_supply":42}`, `[{"id":1,"active":false,"key":"7","value":"10"}]`, "", "invalid_response"},
		{"fraction", `{"total_supply":42}`, `[{"id":1,"active":true,"key":"7","value":1e2}]`, "", "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/contracts/KT1a/storage" {
					require.Equal(t, "100", r.URL.Query().Get("level"))
					_, _ = fmt.Fprint(w, tc.storage)
					return
				}
				require.Equal(t, "/v1/bigmaps/42/historical_keys/100", r.URL.Path)
				require.Equal(t, "7", r.URL.Query().Get("key"))
				_, _ = fmt.Fprint(w, tc.rows)
			})
			defer close()
			got, err := c.GetTokenSupplyAt(context.Background(), rwa.SupplyIdentity{Contract: "KT1a", TokenID: "7"}, 100)
			if tc.reason != "" {
				require.Equal(t, tc.reason, SupplyReason(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestSupplyHistoricalBalanceIsStrictlyTargeted(t *testing.T) {
	valid := `{"account":{"address":"treasury"},"token":{"contract":{"address":"KT1a"},"tokenId":"7","standard":"fa2"},"balance":"9007199254740993"}`
	for _, tc := range []struct {
		name, body, want string
		fail             bool
	}{
		{"exact", "[" + valid + "]", "9007199254740993", false},
		{"absent", "[]", "0", false},
		{"null", "null", "", true},
		{"duplicate", "[" + valid + "," + valid + "]", "", true},
		{"wrong holder", "[" + strings.ReplaceAll(valid, "treasury", "user") + "]", "", true},
		{"wrong token", "[" + strings.ReplaceAll(valid, `"7"`, `"8"`) + "]", "", true},
		{"wrong contract", "[" + strings.ReplaceAll(valid, "KT1a", "KT1b") + "]", "", true},
		{"invalid balance", "[" + strings.ReplaceAll(valid, `"9007199254740993"`, `1e2`) + "]", "", true},
		{"upstream error", "error", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/tokens/historical_balances/100", r.URL.Path)
				q := r.URL.Query()
				require.Equal(t, "treasury", q.Get("account"))
				require.Equal(t, "KT1a", q.Get("token.contract"))
				require.Equal(t, "7", q.Get("token.tokenId"))
				require.Equal(t, "2", q.Get("limit"))
				if tc.body == "error" {
					w.WriteHeader(503)
				}
				_, _ = fmt.Fprint(w, tc.body)
			})
			defer close()
			got, err := c.GetTokenBalanceAt(context.Background(), rwa.SupplyIdentity{Contract: "KT1a", TokenID: "7"}, "treasury", 100)
			if tc.fail {
				require.Error(t, err)
				require.Empty(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestSupplyCampaignPaginationAndDeduplication(t *testing.T) {
	for _, mode := range []string{"complete", "overflow", "repeated page", "missing treasury", "bad campaign", "unknown scope", "empty scope", "http error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/v1/contracts/launchpad/storage":
					require.Equal(t, "100", r.URL.Query().Get("level"))
					_, _ = fmt.Fprint(w, `{"launchLedger":50,"treasuryLedger":51}`)
				case "/v1/bigmaps/50/historical_keys/100":
					if mode == "http error" {
						w.WriteHeader(503)
						return
					}
					q := r.URL.Query()
					offset, _ := strconv.Atoi(q.Get("offset"))
					limit, _ := strconv.Atoi(q.Get("limit"))
					require.Equal(t, "true", q.Get("active"))
					require.Equal(t, "id", q.Get("sort.asc"))
					if mode == "repeated page" {
						offset = 0
					}
					rows := []map[string]any{}
					for i := offset; i < min(offset+limit, 3); i++ {
						value := map[string]any{"tokenContractAddress": "KT1a", "tokenId": "9007199254740993"}
						if mode == "bad campaign" {
							delete(value, "tokenId")
						}
						rows = append(rows, map[string]any{"id": i + 1, "active": true, "key": fmt.Sprint(i), "value": value})
					}
					_ = json.NewEncoder(w).Encode(rows)
				case "/v1/bigmaps/51/historical_keys/100":
					require.Equal(t, "launchTreasury", r.URL.Query().Get("key"))
					if mode == "missing treasury" {
						_, _ = fmt.Fprint(w, "[]")
						return
					}
					_, _ = fmt.Fprint(w, `[{"id":10,"active":true,"key":"launchTreasury","value":"treasury"}]`)
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(404)
				}
			})
			defer close()
			maxRows := 3
			if mode == "overflow" {
				maxRows = 2
			}
			c.WithSupplyLimits(SupplyLimits{PageSize: 2, MaxRows: maxRows})
			pads := []string{"launchpad", "launchpad"}
			if mode == "unknown scope" {
				pads = nil
			}
			if mode == "empty scope" {
				pads = []string{}
			}
			got, err := c.GetLaunchpadTreasuriesAt(context.Background(), pads, 100)
			switch mode {
			case "complete":
				require.NoError(t, err)
				require.Equal(t, SupplyTreasuries{{Contract: "KT1a", TokenID: "9007199254740993"}: []string{"treasury"}}, got)
				require.Equal(t, 4, calls)
			case "empty scope":
				require.NoError(t, err)
				require.Empty(t, got)
				require.Zero(t, calls)
			default:
				require.Error(t, err)
				require.Nil(t, got)
			}
		})
	}
}

func TestSupplyResolveStrictIdentity(t *testing.T) {
	for _, tc := range []struct{ name, body, reason string }{{"huge", `[{"contract":{"address":"KT1a"},"standard":"fa2","tokenId":"900719925474099300000","totalSupply":"900719925474099300000000","firstLevel":1,"lastLevel":2}]`, ""}, {"missing", "[]", "token_not_found"}, {"null", "null", "invalid_response"}, {"ambiguous", "[{},{}]", "identity_ambiguous"}, {"missingID", `[{"contract":{"address":"KT1a"},"standard":"fa2","totalSupply":"0","firstLevel":1,"lastLevel":1}]`, "invalid_response"}, {"fraction", `[{"contract":{"address":"KT1a"},"standard":"fa2","tokenId":1e2,"totalSupply":0,"firstLevel":1,"lastLevel":1}]`, "invalid_response"}} {
		t.Run(tc.name, func(t *testing.T) {
			c, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "KT1a", r.URL.Query().Get("contract"))
				require.Equal(t, "2", r.URL.Query().Get("limit"))
				require.Empty(t, r.URL.Query().Get("tokenId"))
				_, _ = fmt.Fprint(w, tc.body)
			})
			defer close()
			v, err := c.ResolveSingleFA2Token(context.Background(), "KT1a")
			if tc.reason != "" {
				require.Error(t, err)
				require.Equal(t, tc.reason, SupplyReason(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, "900719925474099300000", v.TokenID)
			}
		})
	}
}
func TestSupplyBodyLimitAndHeadHealth(t *testing.T) {
	c, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, strings.Repeat(" ", 100)) })
	defer close()
	c.WithSupplyLimits(SupplyLimits{MaxResponseBytes: 10})
	_, err := c.GetSupplyHead(context.Background())
	require.Error(t, err)
	now := time.Now()
	h := SupplyHead{SupplyBlock: rwa.SupplyBlock{Level: 10, Hash: "hash", Timestamp: now}, ChainID: "chain", KnownLevel: 10, LastSync: now, Synced: true}
	require.NoError(t, h.Validate("chain", now, 120*time.Second))
	require.Error(t, h.Validate("wrong", now, 120*time.Second))
	h.KnownLevel = 13
	require.Equal(t, "indexer_lag", SupplyReason(h.Validate("chain", now, 120*time.Second)))
	h.KnownLevel = 10
	h.LastSync = now.Add(31 * time.Second)
	require.Equal(t, "invalid_timestamp", SupplyReason(h.Validate("chain", now, 120*time.Second)))
}
