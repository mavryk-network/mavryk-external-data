//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"quotes/internal/core/infrastructure/storage/repositories"
)

func TestEquiteezWorkerLockRetriesContention(t *testing.T) {
	db := openGorm(t)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(2) // The supported minimum: lock holder + worker connection.
	lookup := repositories.NewLookupRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	defer unlock()
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- lookup.WithEquiteezLock(ctx, func() error {
			close(locked)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("lock holder did not start")
	}

	// Contention must honor cancellation rather than silently reporting success.
	shortCtx, shortCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer shortCancel()
	err = lookup.WithEquiteezLock(shortCtx, func() error {
		t.Error("callback ran while the other worker owned the lock")
		return nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Release the first worker while the second is waiting. It must execute in
	// this invocation, without waiting for the next (potentially aligned) tick.
	timer := time.AfterFunc(200*time.Millisecond, unlock)
	defer timer.Stop()
	ran := false
	err = lookup.WithEquiteezLock(ctx, func() error {
		ran = true
		return db.WithContext(ctx).Exec("SELECT 1").Error
	})
	require.NoError(t, err)
	require.True(t, ran)
	require.NoError(t, <-holderDone)
}
