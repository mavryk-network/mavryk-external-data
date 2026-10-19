package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEquiteezRecoveryConfig(t *testing.T) {
	cfg := &Config{}
	t.Setenv("CONTRACT_ALLOWLIST_URL", "https://crm.example/allowlist.json")
	t.Setenv("EQUITEEZ_RECOVER_ORDERBOOKS", "KT1first, KT1second")
	t.Setenv("EQUITEEZ_RECOVER_LAUNCH_TOKENS", "KT1primary")
	t.Setenv("EQUITEEZ_HEAD_MAX_AGE_SECONDS", "120")
	t.Setenv("EQUITEEZ_REQUIRED_INDEXES", "token_index,orderbook_index")
	require.NoError(t, overrideWithEnv(cfg))
	require.NoError(t, cfg.Equiteez.ValidateRecovery())
	require.Equal(t, []string{"KT1first", "KT1second"}, cfg.Equiteez.RecoverOrderbooks)
	require.Equal(t, []string{"KT1primary"}, cfg.Equiteez.RecoverLaunchTokens)
	require.Len(t, cfg.Equiteez.ExpectedIndexes(), 2)
	require.Equal(t, 120, cfg.Equiteez.HeadMaxAgeSeconds)
	cfg.RWA.Enabled = true
	cfg.RWA.IntervalSeconds = 1
	cfg.Equiteez.IndexerURL = "https://indexer.example/graphql"
	cfg.Database.MaxOpenConns = 1
	require.ErrorContains(t, cfg.validateRWA(), "max_open_conns")
	cfg.Database.MaxOpenConns = 2
	require.NoError(t, cfg.validateRWA())
	cfg.Equiteez.AllowlistURL = ""
	require.ErrorContains(t, cfg.validateRWA(), "CONTRACT_ALLOWLIST_URL")
}
