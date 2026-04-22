package get_by_token

import "errors"

// ErrTokenNotFound is returned when the token is not supported or unknown for quote storage.
var ErrTokenNotFound = errors.New("token not found")

// IsTokenNotFound reports whether err is or wraps ErrTokenNotFound.
func IsTokenNotFound(err error) bool {
	return err != nil && errors.Is(err, ErrTokenNotFound)
}
