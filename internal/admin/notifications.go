package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/notifications"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type notificationStatus struct {
	WebhookConfigured bool   `json:"webhook_configured"`
	WebhookURL        string `json:"webhook_url"`
	HeartbeatAt       *int64 `json:"heartbeat_at"`
	LastSuccessAt     *int64 `json:"last_success_at"`
	LastError         string `json:"last_error"`
	NextScheduleAt    *int64 `json:"next_schedule_at"`
}

type notificationValues struct {
	Enabled           bool    `json:"enabled"`
	Timezone          string  `json:"timezone"`
	DailyTimes        string  `json:"daily_times"`
	ScheduleGrace     int     `json:"schedule_grace_minutes"`
	QuotaAlertEnabled bool    `json:"quota_alert_enabled"`
	ThresholdPercent  float64 `json:"weekly_threshold_percent"`
}

type notificationSettingsResponse struct {
	Notifications notificationStatus `json:"notifications"`
	Values        notificationValues `json:"values"`
}

func (server *Server) readNotificationSettings(c *gin.Context) {
	payload, err := server.notificationSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read notification settings", err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) notificationSettings(ctx context.Context) (notificationSettingsResponse, error) {
	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return notificationSettingsResponse{}, err
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		return notificationSettingsResponse{}, err
	}
	status, err := server.notificationStatus(ctx)
	if err != nil {
		return notificationSettingsResponse{}, err
	}
	clocks := make([]string, 0, len(config.DailyTimes))
	for _, clock := range config.DailyTimes {
		clocks = append(clocks, clock.String())
	}
	return notificationSettingsResponse{
		Notifications: status,
		Values: notificationValues{
			Enabled: config.Enabled, Timezone: config.TimezoneName,
			DailyTimes:        strings.Join(clocks, ","),
			ScheduleGrace:     int(config.ScheduleGrace / time.Minute),
			QuotaAlertEnabled: config.QuotaAlertEnabled,
			ThresholdPercent:  config.ThresholdPercent,
		},
	}, nil
}

func (server *Server) notificationStatus(ctx context.Context) (notificationStatus, error) {
	state, _, err := notifications.ReadRuntimeState(ctx, server.store)
	if err != nil {
		return notificationStatus{}, err
	}
	status := notificationStatus{
		HeartbeatAt: state.HeartbeatAt, LastSuccessAt: state.LastSuccessAt,
		LastError: notifications.RedactWebhook(state.LastError), NextScheduleAt: state.NextScheduleAt,
	}
	webhook, found, err := server.store.ReadSecret(ctx, "wecom_webhook")
	if err != nil {
		return notificationStatus{}, err
	}
	if found {
		if validated, validationError := notifications.ValidateWebhookURL(webhook); validationError == nil {
			status.WebhookConfigured = true
			status.WebhookURL = validated
		}
	}
	return status, nil
}

type notificationSettingsPayload struct {
	Confirm string `json:"confirm"`
	Values  struct {
		Enabled           *bool    `json:"enabled"`
		Timezone          *string  `json:"timezone"`
		DailyTimes        *string  `json:"daily_times"`
		ScheduleGrace     *int     `json:"schedule_grace_minutes"`
		QuotaAlertEnabled *bool    `json:"quota_alert_enabled"`
		ThresholdPercent  *float64 `json:"weekly_threshold_percent"`
	} `json:"values"`
}

func (server *Server) updateNotificationSettings(c *gin.Context) {
	var body notificationSettingsPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, "请确认保存通知配置", "invalid_request")
		return
	}
	changes, err := server.validatedNotificationChanges(c.Request.Context(), body)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if len(changes) == 0 {
		writeError(c, http.StatusBadRequest, "至少需要提供一项通知配置", "invalid_request")
		return
	}
	if err := server.store.UpdateSettings(c.Request.Context(), changes); err != nil {
		server.internalError(c, "update notification settings", err)
		return
	}
	payload, err := server.notificationSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read updated notification settings", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "企业微信通知配置已保存", "notifications": payload.Notifications,
		"values": payload.Values,
	})
}

func (server *Server) validatedNotificationChanges(
	ctx context.Context,
	body notificationSettingsPayload,
) (map[string]any, error) {
	changes := make(map[string]any)
	if body.Values.Enabled != nil {
		if *body.Values.Enabled {
			configured, err := server.notificationSender.Configured(ctx)
			if err != nil {
				return nil, errors.New("无法确认企业微信 Webhook 配置")
			}
			if !configured {
				return nil, errors.New("启用企业微信通知前必须先配置 Webhook")
			}
		}
		changes["notification.enabled"] = *body.Values.Enabled
	}
	if body.Values.Timezone != nil {
		value := strings.TrimSpace(*body.Values.Timezone)
		if value == "" {
			return nil, errors.New("通知时区不能为空")
		}
		if _, err := time.LoadLocation(value); err != nil {
			return nil, errors.New("通知时区无效")
		}
		changes["notification.timezone"] = value
	}
	if body.Values.DailyTimes != nil {
		clocks, err := notifications.ParseClockTimes(*body.Values.DailyTimes)
		if err != nil {
			return nil, err
		}
		values := make([]string, 0, len(clocks))
		for _, clock := range clocks {
			values = append(values, clock.String())
		}
		changes["notification.daily_times"] = strings.Join(values, ",")
	}
	if body.Values.ScheduleGrace != nil {
		if *body.Values.ScheduleGrace < 0 || *body.Values.ScheduleGrace > 120 {
			return nil, errors.New("定时补发窗口必须在 0 到 120 分钟之间")
		}
		changes["notification.schedule_grace_minutes"] = *body.Values.ScheduleGrace
	}
	if body.Values.QuotaAlertEnabled != nil {
		changes["notification.quota_alert_enabled"] = *body.Values.QuotaAlertEnabled
	}
	if body.Values.ThresholdPercent != nil {
		if *body.Values.ThresholdPercent < 1 || *body.Values.ThresholdPercent > 100 {
			return nil, errors.New("周额度预警阈值必须在 1% 到 100% 之间")
		}
		changes["notification.weekly_threshold_percent"] = *body.Values.ThresholdPercent
	}
	return changes, nil
}

type notificationWebhookPayload struct {
	WebhookURL string `json:"webhook_url"`
	Confirm    string `json:"confirm"`
}

func (server *Server) updateNotificationWebhook(c *gin.Context) {
	var body notificationWebhookPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, "请确认保存企业微信 Webhook", "invalid_request")
		return
	}
	webhook, err := notifications.ValidateWebhookURL(body.WebhookURL)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if err := server.store.WriteSecret(c.Request.Context(), "wecom_webhook", webhook); err != nil {
		server.internalError(c, "save notification webhook", err)
		return
	}
	status, err := server.notificationStatus(c.Request.Context())
	if err != nil {
		server.internalError(c, "read saved notification webhook", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "企业微信 Webhook 已保存", "notifications": status,
	})
}

type notificationConfirmPayload struct {
	Confirm string `json:"confirm"`
}

func (server *Server) clearNotificationWebhook(c *gin.Context) {
	var body notificationConfirmPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "clear" {
		writeError(c, http.StatusBadRequest, "请确认清除企业微信 Webhook", "invalid_request")
		return
	}
	// Delete first: if the following settings update fails, sending remains
	// fail-closed instead of continuing with a credential the operator cleared.
	if err := server.store.DeleteSecret(c.Request.Context(), "wecom_webhook"); err != nil {
		server.internalError(c, "clear notification webhook", err)
		return
	}
	if err := server.store.UpdateSettings(
		c.Request.Context(), map[string]any{"notification.enabled": false},
	); err != nil {
		server.internalError(c, "disable notifications after webhook clear", err)
		return
	}
	status, err := server.notificationStatus(c.Request.Context())
	if err != nil {
		server.internalError(c, "read cleared notification webhook", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "企业微信 Webhook 已清除，通知已关闭", "notifications": status,
	})
}

func (server *Server) sendNotification(c *gin.Context) {
	if server.activity == nil {
		writeError(c, http.StatusServiceUnavailable, "用量查询服务尚未就绪", "usage_not_ready")
		return
	}
	configured, err := server.notificationSender.Configured(c.Request.Context())
	if err != nil {
		server.internalError(c, "check notification webhook", err)
		return
	}
	if !configured {
		writeError(c, http.StatusBadRequest, "尚未配置企业微信 Webhook", "invalid_request")
		return
	}
	settings, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read notification configuration", err)
		return
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	snapshot, err := notifications.CollectSnapshot(c.Request.Context(), server.store, server.activity)
	if err != nil {
		server.internalError(c, "collect manual notification snapshot", err)
		return
	}
	content, err := notifications.BuildMarkdownV2(
		snapshot, config.ShortName+" · 账号额度报告", config.Timezone,
		config.ThresholdPercent, server.now(), nil, nil,
		notifications.UsageCenterURL(config.PublicBaseURL),
	)
	if err != nil {
		writeError(c, http.StatusBadGateway, err.Error(), "notification_send_failed")
		return
	}
	server.deliverNotification(c, content, "账号信息已发送到企业微信群")
}

func (server *Server) testNotification(c *gin.Context) {
	configured, err := server.notificationSender.Configured(c.Request.Context())
	if err != nil {
		server.internalError(c, "check notification webhook", err)
		return
	}
	if !configured {
		writeError(c, http.StatusBadRequest, "尚未配置企业微信 Webhook", "invalid_request")
		return
	}
	settings, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read notification configuration", err)
		return
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	timestamp := server.now().In(config.Timezone).Format("2006-01-02 15:04:05 MST")
	shortName := strings.Join(strings.Fields(config.ShortName), " ")
	content := fmt.Sprintf(
		"# %s · 通知测试\n\n企业微信通知通道连接正常。\n\n> 测试时间：%s",
		shortName,
		timestamp,
	)
	server.deliverNotification(c, content, "测试消息已发送到企业微信群")
}

func (server *Server) deliverNotification(c *gin.Context, content string, successMessage string) {
	result, sendError := server.notificationSender.Send(c.Request.Context(), content)
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	if sendError != nil {
		message := notifications.RedactWebhook(sendError)
		if stateError := server.store.PatchRuntimeState(
			finalizeContext,
			notifications.RuntimeStateName,
			map[string]any{"version": notifications.RuntimeStateVersion, "last_error": message},
		); stateError != nil {
			server.logger.Error("record manual notification failure", zap.Error(stateError))
		}
		writeError(c, http.StatusBadGateway, message, "notification_send_failed")
		return
	}
	now := server.now().Unix()
	if err := server.store.PatchRuntimeState(
		finalizeContext,
		notifications.RuntimeStateName,
		map[string]any{
			"version": notifications.RuntimeStateVersion, "last_success_at": now, "last_error": "",
		},
	); err != nil {
		server.internalError(c, "record manual notification success", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": successMessage, "format": "markdown_v2", "result": result,
	})
}
