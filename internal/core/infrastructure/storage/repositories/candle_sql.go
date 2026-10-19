package repositories

import "fmt"

// candleSource describes which continuous aggregate a chart read targets,
// optionally with a re-bucket width for derived intervals (5m/15m/4h;
// see ADR-0015). Both fields are sourced from a closed switch and are
// safe to fmt.Sprintf into SQL.
type candleSource struct {
	view     string // CA table name (e.g. "token_prices_1m")
	rebucket string // empty for direct reads; time_bucket() spec ("5 minutes") for derived
}

// buildCandleSQL produces the SELECT body shared by both QueryCandles
// implementations; only the caller-bound `where` differs.
//
// Re-bucket reads wrap the CA in time_bucket() and use first()/last() over the
// inner bucket column to preserve open/close ordering (ADR-0015).
//
// ORDER BY bucket DESC so LIMIT keeps the NEWEST buckets, both in latest mode
// and on an over-full window; callers reverse the rows for ascending wire order.
func buildCandleSQL(src candleSource, where string) string {
	if src.rebucket == "" {
		return fmt.Sprintf(`SELECT bucket,
		            open_price,
		            max_price  AS high_price,
		            min_price  AS low_price,
		            close_price,
		            samples
		 FROM %s
		WHERE %s
		ORDER BY bucket DESC`, src.view, where)
	}
	return fmt.Sprintf(`SELECT
		            time_bucket('%s', bucket)        AS bucket,
		            first(open_price, bucket)         AS open_price,
		            max(max_price)                    AS high_price,
		            min(min_price)                    AS low_price,
		            last(close_price, bucket)         AS close_price,
		            sum(samples)                      AS samples
		 FROM %s
		WHERE %s
		GROUP BY 1
		ORDER BY 1 DESC`, src.rebucket, src.view, where)
}
