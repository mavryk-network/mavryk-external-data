package equiteez

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"quotes/internal/config"
	"quotes/internal/core/domain/valuation"
	"sync"
	"testing"
	"time"
)

type scopeMemory struct {
	mu       sync.Mutex
	snapshot AllowlistSnapshot
	found    bool
}

func (m *scopeMemory) AllowlistSnapshot(context.Context, string, *time.Time) (AllowlistSnapshot, error) {
	if !m.found {
		return AllowlistSnapshot{}, errors.New("missing")
	}
	return m.snapshot, nil
}
func (m *scopeMemory) ApplyAllowlist(_ context.Context, _ string, a ContractAllowlist, fresh bool) error {
	if !fresh {
		return nil
	}
	m.found = true
	m.snapshot = AllowlistSnapshot{Document: a, Version: valuation.Fingerprint(a), FetchedAt: time.Now(), SourceStatus: "verified", TreasuryLaunchpads: a.Launchpads}
	return nil
}
func (m *scopeMemory) WithAllowlistLock(_ context.Context, f func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return f()
}
func (m *scopeMemory) RecordAllowlistFailure(context.Context, string) error {
	m.snapshot.SourceStatus = "crm_unavailable"
	return nil
}

func TestSharedScopeRefreshFallbackDoesNotRenewAge(t *testing.T) {
	doc := ContractAllowlist{SchemaVersion: 1, BaseTokens: []string{}, QuoteTokens: []string{}, Orderbooks: []string{}, Launchpads: []string{}}
	fail := false
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if fail {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()
	memory := &scopeMemory{}
	p := &AllowlistProvider{Store: memory, URL: server.URL, Client: NewClient(config.EquiteezConfig{}, &config.APIConfig{OutboundHTTPRetryMaxAttempts: 1}, time.Second, nil)}
	fail = true
	_, err := p.Refresh(context.Background())
	require.Error(t, err)
	fail = false
	s, err := p.Refresh(context.Background())
	require.NoError(t, err)
	require.NotNil(t, s.TreasuryLaunchpads)
	require.Empty(t, s.TreasuryLaunchpads)
	beforeCalls := calls
	cached, err := p.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, beforeCalls+1, calls)
	require.Equal(t, s.Version, cached.Version)
	memory.snapshot.FetchedAt = time.Now().Add(-time.Hour)
	old := memory.snapshot
	fail = true
	fallback, err := p.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, old.FetchedAt, fallback.FetchedAt)
	require.Equal(t, old.Version, fallback.Version)
	require.Equal(t, "crm_unavailable", fallback.SourceStatus)
	afterRestart := &AllowlistProvider{Store: memory, URL: server.URL}
	saved, err := afterRestart.Snapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, fallback, saved)
	fail = false
	recovered, err := p.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, old.Version, recovered.Version)
	require.Equal(t, "verified", recovered.SourceStatus)
	require.True(t, recovered.FetchedAt.After(old.FetchedAt))
}
