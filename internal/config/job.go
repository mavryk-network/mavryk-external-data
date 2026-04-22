package config

// JobConfig controls the quotes collector job defaults.
type JobConfig struct {
	IntervalSeconds int  `yaml:"interval_seconds"`
	Enabled         bool `yaml:"enabled"`
}
