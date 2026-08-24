package admin

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	portalInitialPasswordSecret = "portal_initial_password"
	minimumPortalPasswordLength = 8
	maximumPortalPasswordLength = 128
	legacyPortalPassword        = "123456"
)

func (server *Server) updateInitialPassword(c *gin.Context) {
	var body struct {
		InitialPassword string `json:"initial_password" binding:"required"`
		Confirmation    string `json:"confirmation" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "密码格式无效", "invalid_password")
		return
	}
	if !constantTimeEqual(body.InitialPassword, body.Confirmation) {
		writeError(c, http.StatusBadRequest, "两次输入的初始密码不一致", "password_mismatch")
		return
	}
	length := utf8.RuneCountInString(body.InitialPassword)
	if length < minimumPortalPasswordLength || length > maximumPortalPasswordLength {
		writeError(c, http.StatusBadRequest, "初始密码长度必须为 8 到 128 位", "invalid_password")
		return
	}
	if constantTimeEqual(body.InitialPassword, legacyPortalPassword) {
		writeError(c, http.StatusBadRequest, "不能使用已停用的历史默认密码", "weak_password")
		return
	}
	if err := server.store.WriteSecret(c.Request.Context(), portalInitialPasswordSecret, body.InitialPassword); err != nil {
		server.internalError(c, "update initial portal password", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":    "用户初始密码已安全保存；已有用户密码不会自动变化",
		"configured": true,
	})
}

func (server *Server) rotateManagementKey(c *gin.Context) {
	var body struct {
		NewKey       string `json:"new_key" binding:"required"`
		Confirmation string `json:"confirmation" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "管理密钥格式无效", "invalid_management_key")
		return
	}
	if !constantTimeEqual(body.NewKey, body.Confirmation) {
		writeError(c, http.StatusBadRequest, "两次输入的管理密钥不一致", "management_key_mismatch")
		return
	}
	if err := validateManagementKey(body.NewKey); err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_management_key")
		return
	}
	current, found, err := server.store.ReadSecret(c.Request.Context(), "cpa_management_key")
	if err != nil {
		server.internalError(c, "read current management key", err)
		return
	}
	if !found {
		writeError(c, http.StatusConflict, "当前管理密钥尚未配置", "management_key_not_configured")
		return
	}
	if constantTimeEqual(current, body.NewKey) {
		writeError(c, http.StatusBadRequest, "新管理密钥不能与当前密钥相同", "management_key_unchanged")
		return
	}
	if err := server.store.WriteSecret(c.Request.Context(), "cpa_management_key", body.NewKey); err != nil {
		server.internalError(c, "rotate management key", err)
		return
	}
	server.sessionGeneration.Add(1)
	c.JSON(http.StatusOK, gin.H{
		"message": "管理密钥已更新，请使用新密钥重新进入",
		"result":  gin.H{"rotated": true, "services": 0},
	})
}

func validateManagementKey(value string) error {
	length := utf8.RuneCountInString(value)
	if length < 12 || length > 128 {
		return errors.New("管理密钥长度必须为 12-128 个字符")
	}
	if strings.TrimSpace(value) != value {
		return errors.New("管理密钥不能包含空白或控制字符")
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return errors.New("管理密钥不能包含空白或控制字符")
		}
	}
	return nil
}

func constantTimeEqual(left string, right string) bool {
	leftBytes := []byte(left)
	rightBytes := []byte(right)
	return len(leftBytes) == len(rightBytes) && subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}
