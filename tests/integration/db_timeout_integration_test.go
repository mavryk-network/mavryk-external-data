//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/storage"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewDB_StatementTimeoutOnEveryConnection(t *testing.T) {
	pg, err := pgx.ParseConfig(pgDSN)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		wantMS  int
	}{
		{name: "default", wantMS: 10000},
		{name: "configured", timeout: 50 * time.Millisecond, wantMS: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Database: config.DatabaseConfig{
				Host: pg.Host, Port: strconv.Itoa(int(pg.Port)),
				User: pg.User, Password: pg.Password, Name: pg.Database,
				SSLMode: "disable", MaxOpenConns: 2, MaxIdleConns: 2,
				StatementTimeout: config.DurationYAML(tc.timeout),
			}}
			log := zerolog.Nop()
			db, err := storage.NewDB(cfg, &log)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			pool, err := db.DB.DB()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Hold two connections at once, forcing the pool to open a second
			// session after NewDB's initial ping.
			connections := make([]*sql.Conn, 0, 2)
			for i := 0; i < 2; i++ {
				conn, err := pool.Conn(ctx)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, conn.Close()) })
				connections = append(connections, conn)
				var timeoutMS int
				require.NoError(t, conn.QueryRowContext(ctx,
					"SELECT setting::int FROM pg_settings WHERE name = 'statement_timeout'").Scan(&timeoutMS))
				require.Equal(t, tc.wantMS, timeoutMS)
			}
			if tc.timeout == 0 {
				return
			}
			for _, conn := range connections {
				_, err := conn.ExecContext(ctx, "SELECT pg_sleep(1)")
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr)
				require.Equal(t, "57014", pgErr.Code)
				require.Contains(t, pgErr.Message, "statement timeout")
				// Cancellation must leave the pooled session usable.
				_, err = conn.ExecContext(ctx, "SELECT 1")
				require.NoError(t, err)
			}
		})
	}
}
