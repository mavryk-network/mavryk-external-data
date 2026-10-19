package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDatabaseStatementTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   time.Duration
		wantMS  int64
		wantErr bool
	}{
		{name: "default", wantMS: 10000},
		{name: "minimum", value: time.Millisecond, wantMS: 1},
		{name: "override", value: 1500 * time.Millisecond, wantMS: 1500},
		{name: "maximum", value: (1<<31 - 1) * time.Millisecond, wantMS: 1<<31 - 1},
		{name: "negative cannot disable", value: -time.Second, wantErr: true},
		{name: "submillisecond cannot disable", value: time.Microsecond, wantErr: true},
		{name: "postgres integer overflow", value: (1 << 31) * time.Millisecond, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			setDefaults(c)
			c.Database.StatementTimeout = DurationYAML(tc.value)
			got, err := c.Database.StatementTimeoutMilliseconds()
			if tc.wantErr {
				require.ErrorContains(t, err, "database.statement_timeout")
				require.ErrorContains(t, c.validateDatabase(), "database.statement_timeout")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantMS, got)
			require.NoError(t, c.validateDatabase())
		})
	}
}

func TestDatabaseStatementTimeoutYAMLAndEnv(t *testing.T) {
	t.Setenv("POSTGRES_STATEMENT_TIMEOUT", "")
	c := &Config{}
	require.NoError(t, yaml.Unmarshal([]byte("database:\n  statement_timeout: 3s\n"), c))
	setDefaults(c)
	require.Equal(t, 3*time.Second, c.Database.StatementTimeout.D())

	t.Setenv("POSTGRES_STATEMENT_TIMEOUT", "1500ms")
	require.NoError(t, overrideWithEnv(c))
	setDefaults(c)
	require.Equal(t, 1500*time.Millisecond, c.Database.StatementTimeout.D())

	t.Setenv("POSTGRES_STATEMENT_TIMEOUT", "0s")
	require.NoError(t, overrideWithEnv(c))
	setDefaults(c)
	require.Equal(t, DefaultStatementTimeout, c.Database.StatementTimeout.D())

	t.Setenv("POSTGRES_STATEMENT_TIMEOUT", "invalid")
	require.ErrorContains(t, overrideWithEnv(c), "POSTGRES_STATEMENT_TIMEOUT")
}
