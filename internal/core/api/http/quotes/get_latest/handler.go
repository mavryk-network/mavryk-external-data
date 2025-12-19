package get_latest

import (
	"net/http"
	"quotes/internal/core/application/quotes/get_latest"
	"quotes/internal/core/domain/quotes"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_latest.Action
}

func New(action *get_latest.Action) *Handler {
	return &Handler{action: action}
}

// GetLatestQuote godoc
// @Summary      Get latest quote for MVRK token (legacy endpoint)
// @Description  Retrieve the most recent quote for MVRK token
// @Tags         quotes
// @Accept       json
// @Produce      json
// @Success      200  {object}  quotes.Quote  "Latest quote"
// @Failure      500  {object}  map[string]string  "Internal server error"
// @Router       /quotes/last [get]
func (h *Handler) Handle(c *gin.Context) {
	// Use mvrk token for /quotes/last endpoint (backward compatibility)
	quote, err := h.action.Execute(c.Request.Context(), string(quotes.TokenMVRK))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get latest quote",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, quote)
}
