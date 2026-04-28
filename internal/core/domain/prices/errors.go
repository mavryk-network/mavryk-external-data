package prices

import "errors"

// ErrTokenNotFound is returned when an FT token symbol is not in the registry.
// HTTP layer maps this to 404; never inspect message strings.
var ErrTokenNotFound = errors.New("token not found")

// ErrPairNotFound is the RWA equivalent.
var ErrPairNotFound = errors.New("rwa pair not found")

// ErrSourceNotFound is for unknown source codes (rare; sources are seeded).
var ErrSourceNotFound = errors.New("source not found")
