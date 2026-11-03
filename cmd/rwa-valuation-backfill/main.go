// Command rwa-valuation-backfill reconstructs bounded history using recorded
// CRM/routing evidence. It does not start HTTP or the live collection jobs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"quotes/internal/config"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/interactions/mvkt"
	"quotes/internal/core/infrastructure/interactions/primary"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"
	"syscall"
	"time"
)

func main() { os.Exit(run()) }
func run() int {
	file := flag.String("config", "config.yaml", "configuration path")
	jobID := flag.String("job", "", "stable checkpoint ID; use the same ID to resume")
	contract := flag.String("contract", "", "allowlisted FA2 contract")
	tokenID := flag.String("token-id", "", "explicit token ID (not defaulted to zero)")
	from := flag.Int64("from-level", 0, "inclusive first block")
	to := flag.Int64("to-level", 0, "inclusive last block")
	step := flag.Int64("step", 1, "block sampling step")
	maxSteps := flag.Int("max-steps", 100, "maximum steps this run, 1..1000")
	timeout := flag.Duration("timeout", 10*time.Minute, "total run timeout, at most 30m")
	flag.Parse()
	log := logging.NewLogger()
	if *timeout <= 0 || *timeout > 30*time.Minute {
		log.Error().Msg("invalid timeout")
		return 1
	}
	cfg, err := config.Load(*file)
	if err != nil {
		log.Error().Err(err).Msg("config_load_failed")
		return 1
	}
	id := valuation.SupplyIdentity{Network: cfg.RWA.PricingNetwork(), Contract: *contract, TokenID: *tokenID}
	if !id.Valid() || *jobID == "" || *from <= 0 || *to < *from || *step <= 0 || *maxSteps < 1 || *maxSteps > 1000 {
		log.Error().Msg("explicit job, contract, token-id and bounded block range are required")
		return 1
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, *timeout)
	defer cancel()
	db, err := storage.NewDB(cfg, log)
	if err != nil {
		log.Error().Err(err).Msg("db_connect_failed")
		return 1
	}
	defer func() { _ = db.Close() }()
	lookup := repositories.NewLookupRepository(db.DB)
	tokens, err := lookup.Tokens(ctx)
	if err != nil {
		log.Error().Err(err).Msg("registry_bootstrap_failed")
		return 1
	}
	prices.RegisterTokens(tokens)
	scope := &equiteez.AllowlistProvider{Store: lookup, URL: cfg.Equiteez.AllowlistURL}
	fx := app.NewTokenFXConverter(repositories.NewTokenPriceRepository(db.DB), time.Duration(cfg.Server.FXMaxStalenessSeconds)*time.Second, prices.SourceCoinGecko)
	collector := jobs.NewRWASupplyJob(cfg, repositories.NewRWAValuationRepository(db.DB), lookup, repositories.NewLaunchRepository(db.DB), scope, mvkt.NewClient(cfg.MvKT, &cfg.API), primary.NewHistory(cfg.MvKT, &cfg.API, scope), fx, log)
	report, err := collector.Backfill(ctx, jobs.ValuationBackfillRequest{JobID: *jobID, Identity: id, FromLevel: *from, ToLevel: *to, Step: *step, MaxSteps: *maxSteps})
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		log.Error().Err(encodeErr).Msg("report_failed")
		return 1
	}
	if err != nil {
		log.Error().Err(err).Msg("backfill_incomplete")
		return 1
	}
	return 0
}
