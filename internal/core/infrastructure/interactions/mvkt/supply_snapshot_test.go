package mvkt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSupplySnapshotUsesOneBlockAndOnlyTreasuryBalances(t *testing.T) {
	for _, mode := range []string{"campaign", "no campaign", "shared treasury", "treasury down", "reorg", "excluded exceeds total", "stale block"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			blockReads, balances, launchReads := 0, 0, 0
			c, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				switch r.URL.Path {
				case "/v1/head":
					_ = json.NewEncoder(w).Encode(map[string]any{"level": 102, "hash": "head102", "timestamp": now, "chainId": "chain", "knownLevel": 102, "lastSync": now, "synced": true})
				case "/v1/blocks/100":
					blockReads++
					hash := "block100"
					if mode == "reorg" && blockReads > 1 {
						hash = "other"
					}
					ts := now
					if mode == "stale block" {
						ts = now.Add(-time.Hour)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"level": 100, "hash": hash, "timestamp": ts})
				case "/v1/tokens":
					_, _ = fmt.Fprint(w, `[{"contract":{"address":"KT1asset"},"tokenId":"0","standard":"fa2","totalSupply":"999999999999","firstLevel":1,"lastLevel":102}]`)
				case "/v1/contracts/launchpad/storage", "/v1/contracts/second/storage":
					require.Equal(t, "100", q.Get("level"))
					launchReads++
					if mode == "treasury down" {
						w.WriteHeader(503)
						return
					}
					ledger := 50
					if r.URL.Path == "/v1/contracts/second/storage" {
						ledger = 60
					}
					_, _ = fmt.Fprintf(w, `{"launchLedger":%d,"treasuryLedger":51}`, ledger)
				case "/v1/contracts/KT1asset/storage":
					require.Equal(t, "100", q.Get("level"))
					_, _ = fmt.Fprint(w, `{"total_supply":42}`)
				case "/v1/bigmaps/42/historical_keys/100":
					require.Equal(t, "0", q.Get("key"))
					_, _ = fmt.Fprint(w, `[{"id":1,"active":true,"key":"0","value":"50000000000"}]`)
				case "/v1/bigmaps/50/historical_keys/100", "/v1/bigmaps/60/historical_keys/100":
					if mode == "no campaign" {
						_, _ = fmt.Fprint(w, "[]")
						return
					}
					id := 2
					if r.URL.Path == "/v1/bigmaps/60/historical_keys/100" {
						id = 4
					}
					_, _ = fmt.Fprintf(w, `[{"id":%d,"active":true,"key":"campaign","value":{"tokenContractAddress":"KT1asset","tokenId":"0"}}]`, id)
				case "/v1/bigmaps/51/historical_keys/100":
					_, _ = fmt.Fprint(w, `[{"id":3,"active":true,"key":"launchTreasury","value":"treasury"}]`)
				case "/v1/tokens/historical_balances/100":
					balances++
					require.Equal(t, "treasury", q.Get("account"))
					require.Equal(t, "KT1asset", q.Get("token.contract"))
					require.Equal(t, "0", q.Get("token.tokenId"))
					balance := "10000000000"
					if mode == "excluded exceeds total" {
						balance = "60000000000"
					}
					_, _ = fmt.Fprintf(w, `[{"account":{"address":"treasury"},"token":{"contract":{"address":"KT1asset"},"tokenId":"0","standard":"fa2"},"balance":"%s"}]`, balance)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					w.WriteHeader(404)
				}
			})
			defer close()
			pads := []string{"launchpad"}
			if mode == "shared treasury" {
				pads = append(pads, "second")
			}
			scope, err := PrepareSupplyContext(context.Background(), c, "chain", 2, pads, now, 2*time.Minute)
			if mode == "stale block" {
				require.Equal(t, "indexer_lag", SupplyReason(err))
				return
			}
			require.NoError(t, err)
			amounts, err := ReadSupplyAmounts(context.Background(), c, "KT1asset", scope, func() time.Time { return now }, 2*time.Minute)
			if mode == "reorg" {
				require.Equal(t, "chain_reorg", SupplyReason(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, "50000000000", amounts.Total)
			switch mode {
			case "treasury down", "excluded exceeds total":
				require.Error(t, amounts.CirculatingError)
				require.Empty(t, amounts.Circulating)
			case "no campaign":
				require.NoError(t, amounts.CirculatingError)
				require.Equal(t, amounts.Total, amounts.Circulating)
				require.Zero(t, balances)
			default:
				require.NoError(t, amounts.CirculatingError)
				require.Equal(t, "40000000000", amounts.Circulating)
				require.Equal(t, "10000000000", amounts.Excluded)
				require.Equal(t, 1, balances)
			}
			require.Equal(t, len(pads), launchReads)
		})
	}
}
