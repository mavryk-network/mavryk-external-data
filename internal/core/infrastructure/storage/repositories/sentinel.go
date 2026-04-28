package repositories

import (
	"fmt"
	"time"
)

// sentinel is the result of MIN/MAX(ts) lookups. Found=false when the table is
// empty for the queried token; nil time would conflate "no row" with "row at zero
// time" which never happens in practice but we'd rather not rely on that.
type sentinel struct {
	Found bool
	TS    time.Time
}

// parseSentinel converts a Postgres timestamptz string into a sentinel. Postgres
// returns ISO 8601 without 'T' (e.g. "2025-09-18 14:00:00+00") which time.Parse
// can read with the right layout. Falls back to RFC3339.
func parseSentinel(s string) (sentinel, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05-07",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return sentinel{Found: true, TS: t.UTC()}, nil
		}
	}
	return sentinel{}, fmt.Errorf("unparseable timestamp %q", s)
}
