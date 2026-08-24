package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/gin-gonic/gin"
)

type teamPayload struct {
	ID          string `json:"id"`
	Name        string `json:"name" binding:"required,max=64"`
	Description string `json:"description" binding:"max=200"`
}

func (server *Server) listTeams(c *gin.Context) {
	teams, err := server.store.ListTeams(c.Request.Context())
	if err != nil {
		server.internalError(c, "list teams", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"teams": teams})
}

func (server *Server) createTeam(c *gin.Context) {
	var body teamPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "团队参数无效", "invalid_request")
		return
	}
	team, err := server.store.CreateTeam(c.Request.Context(), body.Name, body.Description)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "团队已创建", "team": team})
}

func (server *Server) updateTeam(c *gin.Context) {
	var body teamPayload
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeError(c, http.StatusBadRequest, "团队参数无效", "invalid_request")
		return
	}
	team, err := server.store.UpdateTeam(
		c.Request.Context(),
		body.ID,
		body.Name,
		body.Description,
	)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "团队已更新", "team": team})
}

func (server *Server) deleteTeam(c *gin.Context) {
	teamID := strings.TrimSpace(c.Query("id"))
	if teamID == "" {
		writeError(c, http.StatusBadRequest, "团队参数无效", "invalid_request")
		return
	}
	team, err := server.store.DeleteTeam(c.Request.Context(), teamID)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "团队已删除", "team": team})
}

func (server *Server) writeControlPlaneError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, "请求参数无效", "invalid_request")
	case errors.Is(err, controlplane.ErrTeamNameExists):
		writeError(c, http.StatusConflict, "团队名称已存在", "team_name_conflict")
	case errors.Is(err, controlplane.ErrTeamNotFound):
		writeError(c, http.StatusNotFound, "团队不存在", "team_not_found")
	case errors.Is(err, controlplane.ErrTeamNotEmpty):
		writeError(c, http.StatusBadRequest, "团队仍有用户，不能删除", "team_not_empty")
	case errors.Is(err, controlplane.ErrTeamMembershipConflict):
		writeError(c, http.StatusConflict, "用户团队归属已变化，请刷新后重试", "team_membership_conflict")
	default:
		server.internalError(c, "mutate control-plane catalog", err)
	}
}
