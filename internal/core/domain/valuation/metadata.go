package valuation

import (
	"encoding/json"
	"math"
	"strconv"
)

// MetadataDecimals separates a legitimate zero from an unresolved unit scale.
// Cache readers and writers must validate with the same rules.
func MetadataDecimals(meta map[string]interface{}) (uint8, bool) {
	var d float64
	switch v := meta["decimals"].(type) {
	case float64:
		d = v
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		d = parsed
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		d = parsed
	default:
		return 0, false
	}
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > 40 || math.Trunc(d) != d {
		return 0, false
	}
	return uint8(d), true
}
