package db_status

import (
	"net/http"
	"quotes/internal/core/infrastructure/storage"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Handler {
	return &Handler{db: db}
}

// Handle godoc
// @Summary      Database / TimescaleDB diagnostics
// @Description  Read-only check: DB reachability, whether timescaledb extension is enabled, and hypertables in schema mev. Does not expose row data.
// @Tags         health
// @Accept       json
// @Produce      json
// @Success      200  {object}  Response  "Diagnostics"
// @Success      503  {object}  map[string]interface{}  "Database unreachable"
// @Router       /health/db [get]
func (h *Handler) Handle(c *gin.Context) {
	st := storage.QueryTimescaleStatus(c.Request.Context(), h.db)
	if !st.DatabaseReachable {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"database_reachable": false,
			"error":                "database ping failed",
		})
		return
	}
	c.JSON(http.StatusOK, st)
}
