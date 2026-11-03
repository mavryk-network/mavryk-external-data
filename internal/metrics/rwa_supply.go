package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var RWASupplyCollections = promauto.NewCounterVec(prometheus.CounterOpts{Name: "rwa_supply_collections_total", Help: "Supply collection outcomes; no asset identities in labels."}, []string{"outcome"})
var RWASupplyLag = promauto.NewGauge(prometheus.GaugeOpts{Name: "rwa_supply_lag_seconds", Help: "Age of the selected supply block at the last tick."})
var RWASupplyCoverage = promauto.NewGauge(prometheus.GaugeOpts{Name: "rwa_supply_collection_coverage_ratio", Help: "Fraction of allowlisted assets collected in the last tick; monetary quality is available in the valuation API."})
