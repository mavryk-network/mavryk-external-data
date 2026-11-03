package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRWASupplyDefaultsAndLimits(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	require.False(t, cfg.RWASupply.Enabled)
	require.Equal(t, 60, cfg.RWASupply.IntervalSeconds)
	require.NoError(t, cfg.validateRWASupply())
	cfg.RWASupply.Enabled = true
	cfg.Equiteez.AllowlistURL = ""
	require.Error(t, cfg.validateRWASupply())
	cfg.Equiteez.AllowlistURL = "https://crm.example/allowlist"
	require.NoError(t, cfg.validateRWASupply())
	cfg.RWASupply.RetentionSeconds = 10
	require.Error(t, cfg.validateRWASupply())
	require.Empty(t, (RWASupplyConfig{}).WithDefaults("another-network").ExpectedChainID)
}
