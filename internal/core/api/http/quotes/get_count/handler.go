package get_count

import (
	"net/http"
	"quotes/internal/core/application/quotes/get_count"
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
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get quotes count",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"count": count,
	})
}
