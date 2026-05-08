package common

import (
	stderrors "errors"
	"net/http"

	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
)

// ErrorResponse is the standard JSON envelope for handler failures.
type ErrorResponse struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// HTTPStatus returns the status code for a core error code.
func HTTPStatus(code coreerrors.Code) int {
	switch code {
	case coreerrors.CodeInvalidArgument:
		return http.StatusBadRequest
	case coreerrors.CodeNotFound:
		return http.StatusNotFound
	case coreerrors.CodeConflict:
		return http.StatusConflict
	case coreerrors.CodeRangeNotSatisfiable:
		return http.StatusRequestedRangeNotSatisfiable
	case coreerrors.CodeUnavailable:
		return http.StatusServiceUnavailable
	case coreerrors.CodeNotImplemented:
		return http.StatusNotImplemented
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
		c.JSON(status, ErrorResponse{Code: string(ce.Code), Message: ce.Message, Details: ce.Details})
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
