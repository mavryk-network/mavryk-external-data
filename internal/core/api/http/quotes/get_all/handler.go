package get_all

import (
	"net/http"
	"quotes/internal/core/application/quotes/get_all"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_all.Action
}

func New(action *get_all.Action) *Handler {
	return &Handler{action: action}
}

func (h *Handler) Handle(c *gin.Context) {
	fromStr := c.Query("from")
	toStr := c.Query("to")
	limitStr := c.Query("limit")

	now := time.Now()
	from := now.Add(-24 * time.Hour)
	to := now

	if fromStr != "" {
		if parsedFrom, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = parsedFrom
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid 'from' parameter format. Use RFC3339 format (e.g., 2023-01-01T00:00:00Z)",
			})
			return
		}
	}

	if toStr != "" {
		if parsedTo, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = parsedTo
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid 'to' parameter format. Use RFC3339 format (e.g., 2023-01-01T00:00:00Z)",
			})
			return
		}
	}

	limit := 0
	if limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid 'limit' parameter. Must be a positive integer",
			})
			return
		}
	}

	if from.After(to) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid time range: 'from' must be before 'to'",
		})
		return
	}

	quotes, err := h.action.Execute(c.Request.Context(), from, to, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get quotes",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, quotes)
}
