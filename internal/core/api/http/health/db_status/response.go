package db_status

// Response documents GET /health/db for Swagger (same shape as storage.TimescaleStatus JSON).
type Response struct {
	DatabaseReachable    bool     `json:"database_reachable"`
	TimescaleInstalled   bool     `json:"timescaledb_installed"`
	TimescaleVersion     string   `json:"timescaledb_version,omitempty"`
	HypertablesMev       []string `json:"hypertables_mev,omitempty"`
	HypertablesQueryNote string   `json:"hypertables_query_note,omitempty"`
}
