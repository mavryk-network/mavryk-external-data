package common

import (
	stderrors "errors"
	"net/http"

	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
)

// ErrorResponse is the standard JSON envelope for handler failures.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// HTTPStatus returns the status code for a core error code.
func HTTPStatus(code coreerrors.Code) int {
	switch code {
	case coreerrors.CodeInvalidArgument:
		return http.StatusBadRequest
	case coreerrors.CodeNotFound:
		return http.StatusNotFound
	case coreerrors.CodeUnavailable:
		return http.StatusServiceUnavailable
	case coreerrors.CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// RespondError writes a JSON error body. Known *coreerrors.Error uses its code and safe message;
// any other error is returned as INTERNAL without leaking details.
func RespondError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	var ce *coreerrors.Error
	if stderrors.As(err, &ce) {
		status := HTTPStatus(ce.Code)
		c.JSON(status, ErrorResponse{Code: string(ce.Code), Message: ce.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, ErrorResponse{
		Code:    string(coreerrors.CodeInternal),
		Message: "An internal error occurred",
	})
}

// RespondErrorWithStatus writes code/message with an explicit HTTP status (e.g. 503 + UNAVAILABLE).
func RespondErrorWithStatus(c *gin.Context, status int, code coreerrors.Code, message string) {
	c.JSON(status, ErrorResponse{Code: string(code), Message: message})
}

// RespondBindingError maps binding / parse failures to INVALID_ARGUMENT without echoing raw parser internals.
func RespondBindingError(c *gin.Context, _ error) {
	RespondError(c, coreerrors.InvalidArgument("Invalid request parameters"))
}
