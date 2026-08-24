package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	webhookHost              = "qyapi.weixin.qq.com"
	webhookPath              = "/cgi-bin/webhook/send"
	maximumWebhookURLLength  = 2048
	maximumWebhookBodyLength = 64 << 10
)

var (
	webhookKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,256}$`)
	webhookRedactor   = regexp.MustCompile(`(?i)https://qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?key=[^\s&"']+`)
)

type SecretStore interface {
	ReadSecret(context.Context, string) (string, bool, error)
}

type WebhookSender struct {
	Store   SecretStore
	Client  *resty.Client
	Timeout time.Duration
}

func ValidateWebhookURL(value string) (string, error) {
	raw := strings.TrimSpace(value)
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > maximumWebhookURLLength || parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), webhookHost) || parsed.Port() != "" ||
		parsed.Path != webhookPath || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("Webhook 地址必须是企业微信消息推送 HTTPS 地址")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query["key"]) != 1 || !webhookKeyPattern.MatchString(query["key"][0]) {
		return "", errors.New("Webhook 地址必须是企业微信消息推送 HTTPS 地址")
	}
	return raw, nil
}

func RedactWebhook(value any) string {
	return webhookRedactor.ReplaceAllString(fmt.Sprint(value), "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=[REDACTED]")
}

func (sender *WebhookSender) Configured(ctx context.Context) (bool, error) {
	_, err := sender.webhookURL(ctx)
	if errors.Is(err, ErrWebhookNotConfigured) || errors.Is(err, ErrWebhookInvalid) {
		return false, nil
	}
	return err == nil, err
}

var (
	ErrWebhookNotConfigured = errors.New("尚未配置企业微信 Webhook")
	ErrWebhookInvalid       = errors.New("企业微信 Webhook 配置无效")
)

func (sender *WebhookSender) webhookURL(ctx context.Context) (string, error) {
	if sender == nil || sender.Store == nil {
		return "", errors.New("企业微信发送器缺少密钥存储")
	}
	value, found, err := sender.Store.ReadSecret(ctx, "wecom_webhook")
	if err != nil {
		return "", fmt.Errorf("读取企业微信 Webhook: %w", err)
	}
	if !found || strings.TrimSpace(value) == "" {
		return "", ErrWebhookNotConfigured
	}
	validated, err := ValidateWebhookURL(value)
	if err != nil {
		return "", ErrWebhookInvalid
	}
	return validated, nil
}

func (sender *WebhookSender) Send(ctx context.Context, content string) (SendResult, error) {
	if len([]byte(content)) > MarkdownV2MaximumSize {
		return SendResult{}, errors.New("企业微信 markdown_v2 内容超过 4096 字节")
	}
	webhook, err := sender.webhookURL(ctx)
	if err != nil {
		return SendResult{}, err
	}
	timeout := sender.Timeout
	if timeout <= 0 || timeout > time.Minute {
		timeout = 10 * time.Second
	}
	client := sender.Client
	if client == nil {
		client = resty.New()
	}
	client = client.Clone().
		SetTimeout(timeout).
		SetRetryCount(0).
		SetResponseBodyLimit(maximumWebhookBodyLength).
		SetRedirectPolicy(resty.NoRedirectPolicy())
	response, err := client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json; charset=utf-8").
		SetBody(map[string]any{
			"msgtype":     "markdown_v2",
			"markdown_v2": map[string]string{"content": content},
		}).
		Post(webhook)
	if err != nil {
		return SendResult{}, fmt.Errorf("企业微信消息发送失败：%s", RedactWebhook(err))
	}
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		return SendResult{}, fmt.Errorf("企业微信消息发送失败：HTTP %d", response.StatusCode())
	}
	var payload struct {
		ErrorCode *int   `json:"errcode"`
		Message   string `json:"errmsg"`
	}
	if err := json.Unmarshal(response.Body(), &payload); err != nil || payload.ErrorCode == nil {
		return SendResult{}, errors.New("企业微信消息发送失败：响应无效")
	}
	if *payload.ErrorCode != 0 {
		message := strings.TrimSpace(RedactWebhook(payload.Message))
		if message == "" {
			message = "响应无效"
		}
		return SendResult{}, fmt.Errorf("企业微信消息发送失败：%s", message)
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "ok"
	}
	return SendResult{ErrorCode: 0, Message: message}, nil
}
