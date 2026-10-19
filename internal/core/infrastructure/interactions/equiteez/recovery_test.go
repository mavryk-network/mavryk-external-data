package equiteez

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"quotes/internal/config"
)

func TestAllowlistRejectsIncompleteDocuments(t *testing.T) {
	valid := `{"schema_version":1,"base_tokens":[],"quote_tokens":[],"orderbooks":[],"launchpads":[]}`
	_, err := ParseAllowlist([]byte(valid))
	require.NoError(t, err)
	for _, body := range []string{
		`{}`, `null`, strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(valid, `"orderbooks":[],`, "", 1), strings.Replace(valid, `"base_tokens":[]`, `"base_tokens":null`, 1),
		strings.Replace(valid, `"base_tokens":[]`, `"base_tokens":["garbage"]`, 1),
		strings.Replace(valid, `"base_tokens":[]`, `"base_tokens":[1]`, 1), valid + valid,
	} {
		_, err := ParseAllowlist([]byte(body))
		require.Error(t, err, body)
	}
}

func TestAllowlistHTTPFailureIsNotAnEmptySnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	client := NewClient(config.EquiteezConfig{}, &config.APIConfig{}, time.Second, nil)
	_, err := client.GetContractAllowlist(context.Background(), srv.URL)
	require.Error(t, err)
}

func TestIndexerReadinessRequiresGenerationAllIndexesAndFreshHead(t *testing.T) {
	now := time.Now().UTC()
	cfg := config.EquiteezConfig{RequiredIndexes: []string{"token_index", "orderbook_index"}}
	base := map[string]any{
		"dipdup_schema": []any{map[string]any{"name": "public", "created_at": now.Add(-time.Hour)}},
		"dipdup_head":   []any{map[string]any{"timestamp": now, "updated_at": now}},
		"dipdup_index":  []any{map[string]any{"name": "token_index", "status": "realtime"}, map[string]any{"name": "orderbook_index", "status": "realtime"}},
	}
	check := func(data map[string]any) (string, error) {
		body, err := json.Marshal(data)
		require.NoError(t, err)
		var status IndexerStatus
		require.NoError(t, json.Unmarshal(body, &status))
		return status.ReadyGeneration(cfg, now)
	}
	generation, err := check(base)
	require.NoError(t, err)
	require.Equal(t, now.Add(-time.Hour).Format(time.RFC3339Nano), generation)
	for _, key := range []string{"dipdup_schema", "dipdup_head", "dipdup_index"} {
		copyMap := make(map[string]any)
		for k, v := range base {
			copyMap[k] = v
		}
		copyMap[key] = []any{}
		_, err := check(copyMap)
		require.Error(t, err, key)
	}
	base["dipdup_index"] = []any{map[string]any{"name": "token_index", "status": "realtime"}, map[string]any{"name": "orderbook_index", "status": "syncing"}}
	_, err = check(base)
	require.Error(t, err)
	base["dipdup_index"] = []any{map[string]any{"name": "token_index", "status": "realtime"}, map[string]any{"name": "orderbook_index", "status": "realtime"}}
	base["dipdup_head"] = []any{map[string]any{"timestamp": now.Add(-time.Hour), "updated_at": now}}
	_, err = check(base)
	require.Error(t, err)
}
