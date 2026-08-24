package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
)

const (
	DefaultUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	DefaultResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	DefaultResetURL        = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"

	maximumResponseBodyBytes = 2 * 1024 * 1024
)

var (
	ErrAuthExpired   = errors.New("official quota authentication expired")
	ErrResetRejected = errors.New("official quota reset was rejected")
)

type Client struct {
	Endpoint             string
	ResetCreditsEndpoint string
	ResetEndpoint        string
	Timeout              time.Duration
}

func (client Client) Fetch(
	ctx context.Context,
	auth OAuthRecord,
	proxyURL string,
) (map[string]any, error) {
	endpoint := strings.TrimSpace(client.Endpoint)
	if endpoint == "" {
		endpoint = DefaultUsageURL
	}
	return client.requestJSON(ctx, http.MethodGet, endpoint, auth, proxyURL, nil)
}

func (client Client) FetchResetCredits(
	ctx context.Context,
	auth OAuthRecord,
	proxyURL string,
) (map[string]any, error) {
	endpoint := strings.TrimSpace(client.ResetCreditsEndpoint)
	if endpoint == "" {
		endpoint = DefaultResetCreditsURL
	}
	return client.requestJSON(ctx, http.MethodGet, endpoint, auth, proxyURL, nil)
}

func (client Client) ConsumeResetCredit(
	ctx context.Context,
	auth OAuthRecord,
	proxyURL string,
	creditID string,
) (map[string]any, error) {
	creditID = strings.TrimSpace(creditID)
	if creditID == "" || len(creditID) > 512 || containsControl(creditID) {
		return nil, errors.New("official quota reset credit is invalid")
	}
	endpoint := strings.TrimSpace(client.ResetEndpoint)
	if endpoint == "" {
		endpoint = DefaultResetURL
	}
	return client.requestJSON(ctx, http.MethodPost, endpoint, auth, proxyURL, map[string]string{
		"redeem_request_id": uuid.NewString(),
		"credit_id":         creditID,
	})
}

func (client Client) requestJSON(
	ctx context.Context,
	method string,
	endpoint string,
	auth OAuthRecord,
	proxyURL string,
	body any,
) (map[string]any, error) {
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Hostname() == "" || !safeEndpoint(parsedEndpoint) {
		return nil, errors.New("official quota endpoint is invalid")
	}
	if strings.TrimSpace(auth.AccessToken) == "" || containsControl(auth.AccessToken) {
		return nil, ErrOAuthMissing
	}
	timeout := client.Timeout
	if timeout < time.Second || timeout > 2*time.Minute {
		timeout = 20 * time.Second
	}
	httpClient := resty.New().
		SetTimeout(timeout).
		SetRetryCount(0).
		SetResponseBodyLimit(maximumResponseBodyBytes).
		SetRedirectPolicy(resty.NoRedirectPolicy())
	if proxyURL != "" {
		httpClient.SetProxy(proxyURL)
	}
	request := httpClient.R().SetContext(ctx).SetHeaders(map[string]string{
		"Authorization":  "Bearer " + auth.AccessToken,
		"Accept":         "application/json",
		"User-Agent":     "cpa-control/2.0",
		"OAI-Language":   "zh-CN",
		"Originator":     "Codex Desktop",
		"Sec-Fetch-Site": "none",
		"Sec-Fetch-Mode": "no-cors",
		"Sec-Fetch-Dest": "empty",
		"Priority":       "u=4, i",
	})
	if auth.AccountID != "" {
		request.SetHeader("ChatGPT-Account-Id", auth.AccountID)
	}
	if body != nil {
		request.SetHeader("Content-Type", "application/json")
		request.SetBody(body)
	}
	response, err := request.Execute(method, endpoint)
	if err != nil {
		return nil, errors.New("request official quota")
	}
	if response.StatusCode() == http.StatusUnauthorized || response.StatusCode() == http.StatusForbidden {
		return nil, ErrAuthExpired
	}
	if response.StatusCode() == http.StatusConflict || response.StatusCode() == http.StatusUnprocessableEntity {
		return nil, ErrResetRejected
	}
	if !response.IsSuccess() {
		return nil, fmt.Errorf("official quota returned HTTP %d", response.StatusCode())
	}
	decoder := json.NewDecoder(bytes.NewReader(response.Body()))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, errors.New("decode official quota response")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("official quota response contains trailing JSON")
	}
	return payload, nil
}

func safeEndpoint(endpoint *url.URL) bool {
	if strings.EqualFold(endpoint.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(endpoint.Scheme, "http") {
		return false
	}
	host := endpoint.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}
