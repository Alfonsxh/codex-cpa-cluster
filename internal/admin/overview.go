package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (server *Server) readOverviewSummary(c *gin.Context) {
	summary, err := server.store.ReadOverviewSummary(c.Request.Context())
	if err != nil {
		server.internalError(c, "read bounded overview summary", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": server.now().Unix(),
		"source":       "control-plane",
		"summary":      summary,
	})
}
