package get_count

import (
	"net/http"
	"quotes/internal/core/application/quotes/get_count"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_count.Action
}

func New(action *get_count.Action) *Handler {
	return &Handler{action: action}
}

func (h *Handler) Handle(c *gin.Context) {
	count, err := h.action.Execute(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to get quotes count",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"count": count,
	})
}
