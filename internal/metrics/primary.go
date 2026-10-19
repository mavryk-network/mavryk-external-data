package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var PrimaryValuationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "primary_valuations_total",
	Help: "Historical Starter valuations by bounded outcome; token identities stay out of labels.",
}, []string{"reason"})
