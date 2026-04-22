package config

// BackfillConfig controls historical quote backfill defaults.
type BackfillConfig struct {
	Enabled      bool   `yaml:"enabled"`
	StartFrom    string `yaml:"start_from"`    // ISO date or RFC3339 (e.g., 2025-09-18 or 2025-09-18T00:00:00Z)
	SleepMs      int    `yaml:"sleep_ms"`      // delay between backfill chunks in milliseconds
	ChunkMinutes int    `yaml:"chunk_minutes"` // size of each backfill window in minutes
}
