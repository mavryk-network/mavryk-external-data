package jobs

import (
	"context"
	"sync"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
)

// defaultPairSyncInterval is how often the Equiteez allowlist is re-read into
// rwa_pairs when no interval is configured. Listings are rare, so hourly keeps
// discovery latency acceptable at negligible cost (one GraphQL query per tick).
const defaultPairSyncInterval = time.Hour

// RWAPairSyncJob keeps `rwa_pairs` in step with the Equiteez allowlist.
//
// Discovery used to run exactly once, synchronously, during startup. That made
// two things restart-only: a newly listed asset never appeared, and a transient
// indexer failure at boot left discovery dead for the whole process lifetime
// (the error was logged and startup continued). Running it on a ticker fixes
// both — a failed tick simply retries on the next one.
//
// runTickerLoop fires the first tick immediately, so this also covers the
// startup sync that main.go used to perform inline.
type RWAPairSyncJob struct {
	cfg      *config.Config
	lookup   *repositories.LookupRepository
	launches *repositories.LaunchRepository
	logger   *zerolog.Logger

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewRWAPairSyncJob(
	cfg *config.Config,
	lookup *repositories.LookupRepository,
	launches *repositories.LaunchRepository,
	log *zerolog.Logger,
) *RWAPairSyncJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	return &RWAPairSyncJob{
		cfg:      cfg,
		lookup:   lookup,
		launches: launches,
		logger:   logging.WithComponent(log, "rwa_pair_sync_job"),
		stopCh:   make(chan struct{}),
	}
}

// Start spawns the ticker. No-op when the RWA module is off or the indexer URL
// is unset — the same guards SyncRWAPairs applies internally.
func (j *RWAPairSyncJob) Start(ctx context.Context) {
	if !j.cfg.RWA.Enabled {
		j.logger.Info().Msg("rwa_pair_sync_disabled_by_config")
		return
	}
	if j.cfg.Equiteez.IndexerURL == "" {
		j.logger.Warn().Msg("rwa_pair_sync_no_indexer_url_skipping")
		return
	}

	interval := time.Duration(j.cfg.RWA.PairSyncIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultPairSyncInterval
	}
	j.logger.Info().Dur("interval", interval).Msg("rwa_pair_sync_job_starting")

	safeGo(&j.wg, j.logger, "rwa_pair_sync", func() {
		runTickerLoop(ctx, j.stopCh, interval, defaultJitter(interval), j.logger, "rwa_pair_sync", func(c context.Context) error {
			return j.syncOnce(c)
		})
	})
}

// Stop signals the goroutine and waits. Idempotent.
func (j *RWAPairSyncJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// syncOnce runs one discovery pass under its own timeout so a hung indexer
// cannot stall the ticker. Errors are logged and returned, never fatal: the
// collector keeps serving whatever rwa_pairs already holds and the next tick
// retries. Returning the error only withholds the tick's last-success stamp.
func (j *RWAPairSyncJob) syncOnce(ctx context.Context) error {
	timeout := time.Duration(j.cfg.API.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Both catalogs are tallied through the same rule the entity-walking ticks
	// use: refreshing one of the two is still progress, and only a tick that
	// refreshed neither withholds the last-success stamp.
	var out tickOutcome
	enabled, err := SyncRWAPairs(syncCtx, j.cfg, j.lookup, j.logger)
	out.record(err)
	if err != nil {
		j.logger.Error().Err(err).Msg("rwa_pair_sync_failed")
		// Fall through: launches are an independent catalog. An orderbook-side
		// failure must not also hide every primary-issuance asset.
	} else {
		j.logger.Debug().Int("enabled_pairs", enabled).Msg("rwa_pair_sync_ok")
	}

	// Primary issuance: tokens sold on the launchpad have no orderbook, so they
	// never produce an rwa_pairs row and would otherwise be absent from GET /v1/rwa.
	if j.launches != nil {
		stored, lErr := SyncRWALaunches(syncCtx, j.cfg, j.launches, j.logger)
		out.record(lErr)
		if lErr != nil {
			j.logger.Error().Err(lErr).Msg("rwa_launch_sync_failed")
		} else {
			j.logger.Debug().Int("launches", stored).Msg("rwa_launch_sync_ok")
		}
	}
	return out.verdict(ctx, "rwa pair sync tick")
}
