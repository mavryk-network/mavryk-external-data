package jobs

import (
	"context"
	"testing"

	"quotes/internal/config"
)

// TestRWAPairSyncJob_StartGuards: the job must no-op (and Stop must not hang)
// when the RWA module is off or the indexer URL is missing. Both paths return
// before touching the repository, so a nil lookup is safe here.
func TestRWAPairSyncJob_StartGuards(t *testing.T) {
	cases := []struct {
		name       string
		enabled    bool
		indexerURL string
	}{
		{"rwa disabled", false, "https://indexer.example/v1/graphql"},
		{"no indexer url", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.RWA.Enabled = c.enabled
			cfg.Equiteez.IndexerURL = c.indexerURL

			j := NewRWAPairSyncJob(cfg, nil, nil)
			j.Start(context.Background())
			done := make(chan struct{})
			go func() { j.Stop(); close(done) }()
			<-done // Stop() must return; a spawned goroutine would block it
		})
	}
}

// TestRWAPairSyncJob_StopIdempotent guards the stopOnce contract — Stop is
// called from the shutdown path and may race with a failed Start.
func TestRWAPairSyncJob_StopIdempotent(t *testing.T) {
	j := NewRWAPairSyncJob(&config.Config{}, nil, nil)
	j.Stop()
	j.Stop()
}
