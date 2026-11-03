package primary

import (
	"context"
	"encoding/json"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/interactions/mvkt"
	"time"
)

// Test transport adapter; production always injects the persisted CRM reader.
type testAllowlistReader struct {
	client *mvkt.Client
	url    string
}

type fixedAllowlist equiteez.AllowlistSnapshot

func (s fixedAllowlist) Snapshot(context.Context) (equiteez.AllowlistSnapshot, error) {
	return equiteez.AllowlistSnapshot(s), nil
}

func (r testAllowlistReader) Snapshot(ctx context.Context) (equiteez.AllowlistSnapshot, error) {
	var a equiteez.ContractAllowlist
	if err := r.client.GetURL(ctx, r.url, &a); err != nil {
		return equiteez.AllowlistSnapshot{}, err
	}
	// Older primary fixtures describe only launchpads. Supply/catalog fixtures
	// exercise ParseAllowlist's full document requirements separately.
	if a.BaseTokens == nil {
		a.BaseTokens = []string{}
	}
	if a.QuoteTokens == nil {
		a.QuoteTokens = []string{}
	}
	if a.Orderbooks == nil {
		a.Orderbooks = []string{}
	}
	b, _ := json.Marshal(a)
	a, err := equiteez.ParseAllowlist(b)
	return equiteez.AllowlistSnapshot{Document: a, Version: valuation.Fingerprint(a), FetchedAt: time.Now(), TreasuryLaunchpads: a.Launchpads}, err
}
