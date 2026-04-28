package config

// RWAConfig controls the Equiteez RWA orderbook collector.
//
// The collector polls Equiteez for currently-known rwa_pairs (loaded from DB at
// startup) and writes one PricePoint per (pair, side) per tick.
type RWAConfig struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	// Concurrency bounds the number of pairs polled in parallel each tick.
	// 0 = unlimited (no semaphore); production should pin to e.g. 4–8.
	Concurrency int `yaml:"concurrency"`
}
