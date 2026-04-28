package common

import (
	"context"
	stderrors "errors"

	"quotes/internal/core/domain/prices"

	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
)

// Bind is the binder signature for the generic Wrap adapter. Returns the parsed
// request payload or an error already wrapped via coreerrors (so it surfaces with
// the right HTTP status).
type Bind[Req any] func(*gin.Context) (Req, error)

// Action runs the application logic for one HTTP request.
type Action[Req any, Res any] func(ctx context.Context, req Req) (Res, error)

// Wrap composes a Bind + Action into a gin.HandlerFunc that:
//  1. binds the request payload (4xx on bind error)
//  2. runs the action
//  3. maps domain errors to HTTP status codes
//  4. JSON-encodes the response
//
// Usage:
//
//	r.GET("/prices/:token", common.Wrap(bindFn, actionFn))
func Wrap[Req any, Res any](bind Bind[Req], action Action[Req, Res]) gin.HandlerFunc {
	return func(c *gin.Context) {
		req, err := bind(c)
		if err != nil {
			// Bind errors can carry sentinel-domain errors (e.g. ErrTokenNotFound)
			// when the binder wants to short-circuit before reaching the action.
			RespondError(c, mapDomainError(err))
			return
		}
		res, err := action(c.Request.Context(), req)
		if err != nil {
			RespondError(c, mapDomainError(err))
			return
		}
		c.JSON(200, res)
	}
}

// mapDomainError translates domain-level errors (ErrTokenNotFound, ...) into
// coreerrors.Error. Everything else is left for the responder to wrap as INTERNAL.
func mapDomainError(err error) error {
	switch {
	case stderrors.Is(err, prices.ErrTokenNotFound):
		return coreerrors.NotFound("Token not found")
	case stderrors.Is(err, prices.ErrPairNotFound):
		return coreerrors.NotFound("RWA pair not found")
	case stderrors.Is(err, prices.ErrSourceNotFound):
		return coreerrors.NotFound("Source not found")
	}
	var ce *coreerrors.Error
	if stderrors.As(err, &ce) {
		return ce
	}
	return coreerrors.Internal("internal error", err)
}
