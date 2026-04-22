package get_count

import (
	"quotes/internal/core/api/http/common"
	"quotes/internal/core/application/quotes/get_count"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/quotes"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_count.Action
}

func New(action *get_count.Action) *Handler {
	return &Handler{action: action}
}

// GetQuotesCount godoc
// @Summary      Get quotes count for MVRK token (legacy endpoint)
// @Description  Retrieve the total number of quotes stored for MVRK token
// @Tags         quotes
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]int64  "Quote count"
// @Failure      500  {object}  map[string]string  "Internal server error"
// @Router       /quotes/count [get]
func (h *Handler) Handle(c *gin.Context) {
	// Use mvrk token for /quotes/count endpoint
	count, err := h.action.Execute(c.Request.Context(), string(quotes.TokenMVRK))
	if err != nil {
		common.RespondError(c, coreerrors.Internal("Unable to load quotes count", err))
		return
	}

	c.JSON(200, gin.H{
		"count": count,
	})
}
