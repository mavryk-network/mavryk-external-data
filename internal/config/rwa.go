package config

// RWAConfig controls the Equiteez RWA orderbook collector.
//
// The collector polls Equiteez for the currently-enabled rwa_pairs — re-read
// from the DB on every tick, so listings and operator toggles apply without a
// restart — and writes one PricePoint per (pair, side) per tick.
type RWAConfig struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	// PairSyncIntervalSeconds is how often the Equiteez allowlist is re-read
	// into rwa_pairs (RWAPairSyncJob). Discovery latency for a newly listed
	// asset is bounded by this. 0 = in-code default (1h).
	PairSyncIntervalSeconds int `yaml:"pair_sync_interval_seconds"`
}
