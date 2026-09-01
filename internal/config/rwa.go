package config

// RWAConfig controls the Equiteez RWA orderbook collector, which writes one
// PricePoint per (pair, side) per tick. Enabled rwa_pairs are re-read from the
// DB every tick, so listings and operator toggles apply without a restart.
type RWAConfig struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	// How often RWAPairSyncJob re-reads the Equiteez allowlist into rwa_pairs;
	// bounds discovery latency for a new listing. 0 = in-code default (1h).
	PairSyncIntervalSeconds int `yaml:"pair_sync_interval_seconds"`
}
