package get_latest

import (
	"net/http"
	"quotes/internal/core/application/quotes/get_latest"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_latest.Action
}

func New(action *get_latest.Action) *Handler {
	return &Handler{action: action}
}

func (h *Handler) Handle(c *gin.Context) {
	quote, err := h.action.Execute(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get latest quote",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, quote)
}
