package mvkt

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"quotes/internal/core/domain/valuation"
	"testing"
	"time"
)

func TestHistoricalSupplyDoesNotTreatOldBlockAsStale(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-365 * 24 * time.Hour)
	client, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/head":
			_ = json.NewEncoder(w).Encode(SupplyHead{SupplyBlock: valuation.SupplyBlock{Level: 102, Hash: "head", Timestamp: now}, ChainID: "test-chain", KnownLevel: 102, LastSync: now, Synced: true})
		case "/v1/blocks/100":
			_ = json.NewEncoder(w).Encode(valuation.SupplyBlock{Level: 100, Hash: "historical", Timestamp: old})
		case "/v1/tokens":
			_, _ = fmt.Fprint(w, `[{"contract":{"address":"asset"},"tokenId":"0","standard":"fa2","firstLevel":1}]`)
		case "/v1/contracts/asset/storage":
			require.Equal(t, "100", r.URL.Query().Get("level"))
			_, _ = fmt.Fprint(w, `{"total_supply":42}`)
		case "/v1/bigmaps/42/historical_keys/100":
			_, _ = fmt.Fprint(w, `[{"id":1,"active":true,"key":"0","value":"50000000000"}]`)
		default:
			t.Errorf("unexpected historical path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	defer close()
	scope, err := HistoricalSupplyContext(context.Background(), client, "test-chain", 100, []string{})
	require.NoError(t, err)
	amounts, err := ReadSupplyAmounts(context.Background(), client, "asset", scope, func() time.Time { return old }, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "50000000000", amounts.Circulating)
	_, err = PrepareSupplyContext(context.Background(), client, "test-chain", 2, []string{}, now, 2*time.Minute)
	require.Equal(t, "indexer_lag", SupplyReason(err))
}

func TestValuationMetadataUsesHistoricalPointerAndExactIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, key, token, scale string
		want                    bool
	}{{"valid", "0", "0", "36", true}, {"wrong key", "1", "0", "36", false}, {"wrong value", "0", "1", "36", false}, {"unknown decimals", "0", "0", "", false}, {"fractional decimals", "0", "0", "362e35", false}} {
		t.Run(tc.name, func(t *testing.T) {
			client, close := newHistoryClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/contracts/asset/storage" {
					require.Equal(t, "100", r.URL.Query().Get("level"))
					_, _ = fmt.Fprint(w, `{"token_metadata":43}`)
					return
				}
				require.Equal(t, "/v1/bigmaps/43/historical_keys/100", r.URL.Path)
				_, _ = fmt.Fprintf(w, `[{"id":1,"active":true,"key":"%s","value":{"token_id":"%s","token_info":{"decimals":"%s","symbol":"54455354"}}}]`, tc.key, tc.token, tc.scale)
			})
			defer close()
			meta, symbol, err := client.SupplyMetadataAt(context.Background(), valuation.SupplyIdentity{Contract: "asset", TokenID: "0"}, valuation.SupplyBlock{Level: 100})
			if tc.want {
				require.NoError(t, err)
				require.Equal(t, 6, *meta.Decimals)
				require.Equal(t, "TEST", symbol)
				require.NotEmpty(t, meta.Version)
			} else {
				require.Error(t, err)
				require.Nil(t, meta.Decimals)
			}
		})
	}
}
