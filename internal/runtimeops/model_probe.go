package runtimeops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

const modelProbeBodyLimit = 1 << 20

var modelIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

type ModelProbeStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error)
}

type ModelProbeRuntime interface {
	List(context.Context) ([]Service, error)
}

type ModelProbeError struct {
	Code    string
	Message string
}

func (err *ModelProbeError) Error() string { return err.Message }

type AccountModels struct {
	Account string   `json:"account"`
	Models  []string `json:"models"`
}

type ModelProbeResult struct {
	Account        string `json:"account"`
	Model          string `json:"model"`
	Success        bool   `json:"success"`
	ElapsedMS      int64  `json:"elapsed_ms"`
	CheckedAt      int64  `json:"checked_at"`
	UpstreamStatus int    `json:"upstream_status"`
	Code           string `json:"code"`
	Message        string `json:"message"`
}

// ModelProbe sends a fixed, small generation request to one existing CPA. It
// never changes user routes or returns internal credentials/upstream bodies.
type ModelProbe struct {
	store   ModelProbeStore
	runtime ModelProbeRuntime
	client  *http.Client
	mu      sync.Mutex
	active  map[string]bool
}

func NewModelProbe(store ModelProbeStore, runtime ModelProbeRuntime, client *http.Client) *ModelProbe {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// This hop stays on the private CPA network. The account itself owns
		// its configured outbound proxy; never forward internal Keys to an
		// ambient HTTP_PROXY on the Admin process.
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	boundedClient := *client
	boundedClient.Timeout = 25 * time.Second
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ModelProbe{store: store, runtime: runtime, client: &boundedClient, active: make(map[string]bool)}
}

func (probe *ModelProbe) acquire(account string) (func(), error) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.active[account] || len(probe.active) >= 3 {
		return nil, &ModelProbeError{"model_test_busy", "已有模型测试正在执行，请稍后重试"}
	}
	probe.active[account] = true
	return func() { probe.mu.Lock(); delete(probe.active, account); probe.mu.Unlock() }, nil
}

func (probe *ModelProbe) target(ctx context.Context, account string) (string, string, error) {
	normalized, err := controlplane.NormalizeAccountID(account)
	if err != nil || normalized != account {
		return "", "", &ModelProbeError{"invalid_account", "账号标识无效"}
	}
	accounts, err := probe.store.ReadAccounts(ctx)
	if err != nil {
		return "", "", &ModelProbeError{"model_test_unavailable", "暂时无法读取账号目录"}
	}
	found := false
	for _, candidate := range accounts {
		found = found || candidate.ID == account
	}
	if !found {
		return "", "", &ModelProbeError{"account_not_found", "账号不存在"}
	}
	services, err := probe.runtime.List(ctx)
	if err != nil {
		return "", "", &ModelProbeError{"model_test_unavailable", "暂时无法读取账号运行状态"}
	}
	running := false
	for _, service := range services {
		if service.Service == "cliproxy-"+account {
			running = service.State == "running"
		}
	}
	if !running {
		return "", "", &ModelProbeError{"account_not_running", "账号容器未运行，请先启动容器"}
	}
	keys, err := probe.store.ReadInternalKeys(ctx)
	if err != nil {
		return "", "", &ModelProbeError{"model_test_unavailable", "暂时无法读取测试凭据"}
	}
	users := make([]string, 0, len(keys))
	for user, key := range keys {
		if key.Status == "active" && strings.TrimSpace(key.Key) != "" {
			users = append(users, user)
		}
	}
	sort.Strings(users)
	if len(users) == 0 {
		return "", "", &ModelProbeError{"model_test_no_credentials", "尚无可用的内部凭据，请先创建一名用户；无需绑定到此账号"}
	}
	// The account projection installs all active internal Keys on every CPA,
	// including accounts with no routed users, just as runtime readiness probes do.
	return "http://cliproxy-" + account + ":8317", keys[users[0]].Key, nil
}

func (probe *ModelProbe) request(ctx context.Context, base, key, path string, body []byte) (int, []byte, error) {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := probe.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, modelProbeBodyLimit+1))
	if err == nil && len(payload) > modelProbeBodyLimit {
		err = errors.New("model response too large")
	}
	return response.StatusCode, payload, err
}

func (probe *ModelProbe) Models(ctx context.Context, account string) (AccountModels, error) {
	release, err := probe.acquire(account)
	if err != nil {
		return AccountModels{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base, key, err := probe.target(ctx, account)
	if err != nil {
		return AccountModels{}, err
	}
	status, payload, err := probe.request(ctx, base, key, "/v1/models", nil)
	if err != nil || status != http.StatusOK {
		return AccountModels{}, &ModelProbeError{"model_list_unavailable", "无法读取此账号的模型列表，请检查容器和授权状态后重试"}
	}
	var decoded struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &decoded) != nil {
		return AccountModels{}, &ModelProbeError{"invalid_model_list", "账号返回了无效的模型列表"}
	}
	result := AccountModels{Account: account, Models: []string{}}
	seen := make(map[string]bool)
	for _, model := range decoded.Data {
		if modelIDPattern.MatchString(model.ID) && !seen[model.ID] && len(result.Models) < 200 {
			result.Models = append(result.Models, model.ID)
			seen[model.ID] = true
		}
	}
	sort.Strings(result.Models)
	return result, nil
}

func (probe *ModelProbe) Test(ctx context.Context, account, model string) (ModelProbeResult, error) {
	if !modelIDPattern.MatchString(model) {
		return ModelProbeResult{}, &ModelProbeError{"invalid_model", "请选择有效的模型"}
	}
	release, err := probe.acquire(account)
	if err != nil {
		return ModelProbeResult{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	base, key, err := probe.target(ctx, account)
	if err != nil {
		return ModelProbeResult{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": "Reply with exactly OK.", "max_output_tokens": 64,
		"reasoning": map[string]string{"effort": "low"}, "stream": false,
	})
	started := time.Now()
	status, payload, requestError := probe.request(ctx, base, key, "/v1/responses", body)
	result := ModelProbeResult{Account: account, Model: model, ElapsedMS: time.Since(started).Milliseconds(),
		CheckedAt: time.Now().Unix(), UpstreamStatus: status, Code: "model_test_failed", Message: "模型未完成生成，请查看账号日志"}
	if requestError != nil {
		result.Code, result.Message = "model_connection_failed", "模型连接失败或响应不完整，请检查账号网络和日志"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(requestError, context.DeadlineExceeded) {
			result.Code, result.Message = "model_test_timeout", "模型通信测试超时（25 秒），请稍后重试"
		}
		return result, nil
	}
	if status != http.StatusOK {
		switch status {
		case 401, 403:
			result.Code, result.Message = "model_auth_failed", "账号授权失败，请检查 OAuth 状态"
		case 400, 404:
			result.Code, result.Message = "model_not_supported", "此账号无法使用所选模型或测试参数"
		case 429:
			result.Code, result.Message = "model_rate_limited", "账号额度不足或请求受限，请检查额度后重试"
		default:
			result.Code, result.Message = "model_upstream_error", "模型服务暂时不可用，请稍后重试或查看账号日志"
		}
		return result, nil
	}
	var decoded struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(payload, &decoded) != nil || decoded.Status != "completed" {
		return result, nil
	}
	for _, item := range decoded.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				result.Success, result.Code, result.Message = true, "model_test_passed", "模型已完成生成并返回文本"
				return result, nil
			}
		}
	}
	return result, nil
}
