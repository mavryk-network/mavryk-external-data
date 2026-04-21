package storage

import (
	"context"

	"gorm.io/gorm"
)

// TimescaleStatus is a read-only snapshot for diagnostics (e.g. GET /health/db).
type TimescaleStatus struct {
	DatabaseReachable    bool     `json:"database_reachable"`
	TimescaleInstalled   bool     `json:"timescaledb_installed"`
	TimescaleVersion     string   `json:"timescaledb_version,omitempty"`
	HypertablesMev       []string `json:"hypertables_mev,omitempty"`
	HypertablesQueryNote string   `json:"hypertables_query_note,omitempty"`
}

// QueryTimescaleStatus runs lightweight catalog queries. It does not modify data.
func QueryTimescaleStatus(ctx context.Context, db *gorm.DB) TimescaleStatus {
	out := TimescaleStatus{}
	sqlDB, err := db.DB()
	if err != nil {
		return out
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return out
	}
	out.DatabaseReachable = true

	var extRows []struct {
		Extversion string `gorm:"column:extversion"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'`).Scan(&extRows).Error; err != nil || len(extRows) == 0 {
		return out
	}
	out.TimescaleInstalled = true
	out.TimescaleVersion = extRows[0].Extversion

	var htRows []struct {
		Name string `gorm:"column:hypertable_name"`
	}
	err = db.WithContext(ctx).Raw(`
		SELECT hypertable_name
		FROM timescaledb_information.hypertables
		WHERE hypertable_schema = 'mev'
		ORDER BY hypertable_name
	`).Scan(&htRows).Error
	if err != nil {
		out.HypertablesQueryNote = err.Error()
		return out
	}
	for _, r := range htRows {
		out.HypertablesMev = append(out.HypertablesMev, r.Name)
	}
	return out
}
