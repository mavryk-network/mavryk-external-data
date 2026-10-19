package config

import "testing"

func TestMvKTConfig_BasenetDefaultAndSharedOverride(t *testing.T) {
	var cfg Config
	setDefaults(&cfg)
	if cfg.MvKT.IndexerURL != DefaultMvKTIndexerURL {
		t.Fatalf("MvKT did not default to Basenet: %q", cfg.MvKT.IndexerURL)
	}
	if err := cfg.validateMvKT(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MVKT_INDEXER_URL", "http://localhost:8080/v1/")
	if err := overrideWithEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MvKT.IndexerURL != "http://localhost:8080/v1" {
		t.Fatalf("shared URL override ignored: %q", cfg.MvKT.IndexerURL)
	}
	if err := cfg.validateMvKT(); err != nil {
		t.Fatal(err)
	}
}

func TestMvKTConfig_RejectsInvalidBaseURL(t *testing.T) {
	for _, address := range []string{"", "file:///tmp", "https://user:secret@example.com", "https://example.com?q=1", "https://example.com#fragment"} {
		cfg := Config{MvKT: MvKTConfig{IndexerURL: address}}
		if err := cfg.validateMvKT(); err == nil {
			t.Fatalf("invalid MvKT base URL accepted: %q", address)
		}
	}
}
