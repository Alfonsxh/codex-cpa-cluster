package admin

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/branding"
	"github.com/gin-gonic/gin"
)

type brandingPayload struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	DataBase64  string `json:"data_base64"`
	Confirm     string `json:"confirm" binding:"required"`
}

func (server *Server) brandingLogo(c *gin.Context) {
	asset, found, err := server.store.ReadBrandingAsset(c.Request.Context(), "logo")
	if err != nil {
		server.internalError(c, "read branding logo", err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, "未配置自定义 Logo", "not_found")
		return
	}
	c.Header("ETag", `"`+asset.SHA256+`"`)
	c.Data(http.StatusOK, asset.ContentType, asset.Content)
}

func (server *Server) updateBrandingLogo(c *gin.Context) {
	var body brandingPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" || strings.TrimSpace(body.DataBase64) == "" {
		writeError(c, http.StatusBadRequest, "Logo 参数无效", "invalid_request")
		return
	}
	content, err := base64.StdEncoding.Strict().DecodeString(body.DataBase64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Logo 文件编码无效", "invalid_request")
		return
	}
	logo, err := branding.ValidateLogo(body.Filename, body.ContentType, content)
	if err != nil {
		if errors.Is(err, branding.ErrInvalidLogo) {
			writeError(c, http.StatusBadRequest, "Logo 文件无效", "invalid_logo")
			return
		}
		server.internalError(c, "validate branding logo", err)
		return
	}
	asset, err := server.store.WriteBrandingAsset(
		c.Request.Context(),
		"logo",
		logo.Filename,
		logo.ContentType,
		logo.Content,
	)
	if err != nil {
		server.internalError(c, "write branding logo", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Logo 已更新",
		"logo": gin.H{
			"custom":       true,
			"url":          "/branding/logo",
			"content_type": asset.ContentType,
			"sha256":       asset.SHA256,
		},
	})
}

func (server *Server) deleteBrandingLogo(c *gin.Context) {
	var body struct {
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "reset" {
		writeError(c, http.StatusBadRequest, "请确认恢复默认 Logo", "invalid_request")
		return
	}
	_, existed, err := server.store.ReadBrandingAsset(c.Request.Context(), "logo")
	if err != nil {
		server.internalError(c, "read branding logo before reset", err)
		return
	}
	if err := server.store.DeleteBrandingAsset(c.Request.Context(), "logo"); err != nil {
		server.internalError(c, "delete branding logo", err)
		return
	}
	message := "当前已使用默认 Logo"
	if existed {
		message = "已恢复默认 Logo"
	}
	c.JSON(http.StatusOK, gin.H{
		"message": message,
		"logo": gin.H{
			"custom": false,
		},
	})
}
