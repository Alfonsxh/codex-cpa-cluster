package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/go-resty/resty/v2"
)

const testWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder"

func TestValidateWebhookURLAndRedaction(t *testing.T) {
	for _, value := range []string{
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder",
		"https://example.com/cgi-bin/webhook/send?key=test-placeholder",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder&debug=1",
	} {
		if _, err := ValidateWebhookURL(value); err == nil {
			t.Fatalf("invalid webhook accepted: %q", value)
		}
	}
	if got, err := ValidateWebhookURL(testWebhook); err != nil || got != testWebhook {
		t.Fatalf("valid webhook = (%q, %v)", got, err)
	}
	redacted := RedactWebhook("failed " + testWebhook)
	if strings.Contains(redacted, "test-placeholder") || !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("redacted webhook = %q", redacted)
	}
}

func TestWebhookSenderUsesMarkdownV2WithoutRetriesOrRedirects(t *testing.T) {
	secret := &fakeSecretStore{value: testWebhook, found: true}
	transport := &recordingTransport{responses: []transportResponse{
		{status: http.StatusOK, body: `{"errcode":0,"errmsg":"ok"}`},
		{status: http.StatusOK, body: `{"errcode":93000,"errmsg":"invalid webhook"}`},
	}}
	sender := &WebhookSender{
		Store: secret, Client: resty.New().SetTransport(transport), Timeout: 3 * time.Second,
	}
	result, err := sender.Send(context.Background(), "# test")
	if err != nil || result.ErrorCode != 0 || result.Message != "ok" {
		t.Fatalf("Send = (%#v, %v)", result, err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.URL.String() != testWebhook || request.Method != http.MethodPost {
		t.Fatalf("request = %s %s", request.Method, request.URL)
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.bodies[0], &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload["msgtype"] != "markdown_v2" || strings.Contains(string(transport.bodies[0]), "key=") {
		t.Fatalf("request payload = %s", transport.bodies[0])
	}
	if _, err := sender.Send(context.Background(), "# test"); err == nil || !strings.Contains(err.Error(), "invalid webhook") {
		t.Fatalf("WeCom application error = %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("Resty retried non-idempotent webhook: %d requests", len(transport.requests))
	}
}

func TestFencedSenderRejectsStaleLeaseBeforeWebhook(t *testing.T) {
	sentinel := errors.New("lease generation lost")
	inner := &fakeSender{configured: true}
	fence := &notificationWriteFenceStub{err: sentinel}
	sender, err := NewFencedSender(inner, fence)
	if err != nil {
		t.Fatalf("NewFencedSender: %v", err)
	}
	configured, err := sender.Configured(context.Background())
	if err != nil || !configured || fence.calls != 0 {
		t.Fatalf("Configured = (%v, %v), fence calls %d", configured, err, fence.calls)
	}
	if _, err := sender.Send(context.Background(), "# test"); !errors.Is(err, sentinel) {
		t.Fatalf("fenced Send error = %v", err)
	}
	if fence.calls != 1 || len(inner.contents) != 0 {
		t.Fatalf("rejected send = fence calls %d contents %#v", fence.calls, inner.contents)
	}
}

func TestQuotaRowsAndMarkdownMatchV1Contract(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	snapshot := Snapshot{Accounts: []AccountSnapshot{
		testAccountSnapshot("cpa-10", 100, "常规周限额"),
		testAccountSnapshot("cpa-3", 5, "常规周限额"),
		testAccountSnapshot("cpa-2", 10, "常规周限额"),
		testAccountSnapshot("cpa-1", 10, "常规周限额"),
	}}
	snapshot.Accounts[1].Quota.Status = "error"
	rows := QuotaRows(snapshot, 90, nil)
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.Account+":"+row.Level)
	}
	want := []string{"cpa-1:normal", "cpa-2:normal", "cpa-10:exhausted", "cpa-3:unavailable"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("row ordering = %v", got)
	}
	content, err := BuildMarkdownV2(
		snapshot, "CPA 账号额度报告", now.Location(), 90, now, nil, nil,
		UsageCenterURL("http://cpa.example.com"),
	)
	if err != nil {
		t.Fatalf("BuildMarkdownV2: %v", err)
	}
	for _, expected := range []string{
		"| CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
		"> 应用地址：[http://cpa.example.com/usage/](http://cpa.example.com/usage/)",
		"🔴 cpa-10 · 常规周限额", "100% | 3 | 2",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("markdown is missing %q:\n%s", expected, content)
		}
	}
	if !(strings.Index(content, "cpa-1") < strings.Index(content, "cpa-2") &&
		strings.Index(content, "cpa-2") < strings.Index(content, "cpa-10") &&
		strings.Index(content, "cpa-10") < strings.Index(content, "cpa-3")) {
		t.Fatalf("markdown row ordering:\n%s", content)
	}
}

func TestMarkdownFiltersGPT53AndEnforcesOfficialLimit(t *testing.T) {
	snapshot := Snapshot{Accounts: []AccountSnapshot{
		testAccountSnapshot("alpha", 90, "常规周限额"),
	}}
	snapshot.Accounts[0].Quota.WeeklyWindows = append(
		snapshot.Accounts[0].Quota.WeeklyWindows,
		quota.WeeklyWindow{Key: "default:secondary_window", Label: "GPT-5.3-Codex-Spark", UsedPercent: 40},
	)
	content, err := BuildMarkdownV2(snapshot, "报告", time.UTC, 90, time.Unix(1, 0), nil, nil, "")
	if err != nil || strings.Contains(content, "GPT-5.3") || strings.Count(content, "alpha") != 1 {
		t.Fatalf("filtered markdown = (%q, %v)", content, err)
	}
	large := Snapshot{Accounts: make([]AccountSnapshot, 0, 100)}
	for index := 0; index < 100; index++ {
		large.Accounts = append(large.Accounts, testAccountSnapshot(
			strings.Repeat("account-long-name-", 2)+string(rune('a'+index%26)), 25, "常规周限额",
		))
	}
	if _, err := BuildMarkdownV2(large, "报告", time.UTC, 90, time.Now(), nil, nil, ""); err == nil || !strings.Contains(err.Error(), "4096") {
		t.Fatalf("large markdown error = %v", err)
	}
}

func TestReadRuntimeStateRecoversInvalidNestedMaps(t *testing.T) {
	store := newFakeStore()
	store.runtime[RuntimeStateName] = json.RawMessage(`{
		"version":1,"scheduled":[],"quota_alerts":"invalid","quota_windows":null,
		"last_error":"previous failure"
	}`)
	state, found, err := ReadRuntimeState(context.Background(), store)
	if err != nil || !found {
		t.Fatalf("ReadRuntimeState = (%#v, %v, %v)", state, found, err)
	}
	if state.Scheduled == nil || len(state.Scheduled) != 0 || state.QuotaAlerts == nil ||
		state.QuotaWindows == nil || state.LastError != "previous failure" {
		t.Fatalf("recovered state = %#v", state)
	}
}

func TestWorkerScheduledReportsAreIdempotentAcrossOverlappingSlotsAndTimezones(t *testing.T) {
	store, activity, sender := workerFixtures()
	store.settings["notification.daily_times"] = "09:00,09:05"
	worker := &Worker{Store: store, Activity: activity, Sender: sender}
	worker.Now = fixedNow("Asia/Shanghai", 2026, 7, 20, 9, 6, 0)
	first, err := worker.RunOnce(context.Background())
	if err != nil || strings.Join(first.Sent, ",") != "scheduled" {
		t.Fatalf("first run = (%#v, %v)", first, err)
	}
	worker.Now = fixedNow("Asia/Shanghai", 2026, 7, 20, 9, 6, 30)
	second, err := worker.RunOnce(context.Background())
	if err != nil || len(second.Sent) != 0 || len(sender.contents) != 1 {
		t.Fatalf("second run = (%#v, %v), sends=%d", second, err, len(sender.contents))
	}
	state := store.notificationState(t)
	if len(state.Scheduled) != 2 {
		t.Fatalf("scheduled records = %#v", state.Scheduled)
	}
	store.settings["notification.timezone"] = "UTC"
	store.settings["notification.daily_times"] = "09:00"
	worker.Now = fixedNow("UTC", 2026, 7, 20, 9, 0, 0)
	third, err := worker.RunOnce(context.Background())
	if err != nil || strings.Join(third.Sent, ",") != "scheduled" || len(sender.contents) != 2 {
		t.Fatalf("timezone run = (%#v, %v), sends=%d", third, err, len(sender.contents))
	}
}

func TestWorkerQuotaStateMachineDeduplicatesRecoversAndRearms(t *testing.T) {
	store, activity, sender := workerFixtures()
	store.settings["notification.daily_times"] = "23:59"
	worker := &Worker{Store: store, Activity: activity, Sender: sender}
	setQuota(store, "alpha", 89.99, "ok")
	runWorkerAt(t, worker, 10, 0, nil)
	setQuota(store, "alpha", 90, "ok")
	runWorkerAt(t, worker, 10, 1, []string{"quota_alert"})
	runWorkerAt(t, worker, 10, 2, nil)
	setQuota(store, "alpha", 100, "ok")
	runWorkerAt(t, worker, 10, 3, []string{"quota_exhausted"})
	setQuota(store, "alpha", 100, "error")
	runWorkerAt(t, worker, 10, 4, nil)
	if _, found := store.notificationState(t).QuotaAlerts["alpha|default:primary_window"]; !found {
		t.Fatal("unavailable observation cleared an active alert")
	}
	setQuota(store, "alpha", 89, "ok")
	runWorkerAt(t, worker, 10, 5, []string{"quota_recovered"})
	if len(store.notificationState(t).QuotaAlerts) != 0 {
		t.Fatalf("recovery did not clear alerts: %#v", store.notificationState(t).QuotaAlerts)
	}
	setQuota(store, "alpha", 90, "ok")
	runWorkerAt(t, worker, 10, 6, []string{"quota_alert"})
	if len(sender.contents) != 4 || !strings.Contains(sender.contents[0], "🟠 达到预警") ||
		!strings.Contains(sender.contents[2], "🟢 额度恢复") {
		t.Fatalf("sent contents = %#v", sender.contents)
	}
}

func TestWorkerRefreshBaselineAdvancesEvenWhenWebhookFails(t *testing.T) {
	store, activity, sender := workerFixtures()
	store.settings["notification.daily_times"] = "23:59"
	worker := &Worker{Store: store, Activity: activity, Sender: sender}
	setQuota(store, "alpha", 40, "ok")
	runWorkerAt(t, worker, 10, 0, nil)
	setQuota(store, "alpha", 3, "ok")
	sender.sendError = errors.New("temporary webhook failure")
	worker.Now = fixedNow("Asia/Shanghai", 2026, 7, 20, 10, 1, 0)
	if _, err := worker.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("failed send error = %v", err)
	}
	state := store.notificationState(t)
	if state.QuotaWindows["alpha|default:primary_window"].UsedPercent != 3 ||
		!strings.Contains(state.LastError, "temporary") {
		t.Fatalf("state after failed refresh = %#v", state)
	}
	sender.sendError = nil
	runWorkerAt(t, worker, 10, 2, nil)
	if len(sender.contents) != 0 {
		t.Fatalf("failed refresh was retried from stale baseline: %#v", sender.contents)
	}
}

func TestWorkerFailedRecoveryRetainsAlertAndRetries(t *testing.T) {
	store, activity, sender := workerFixtures()
	store.settings["notification.daily_times"] = "23:59"
	worker := &Worker{Store: store, Activity: activity, Sender: sender}
	setQuota(store, "alpha", 100, "ok")
	runWorkerAt(t, worker, 10, 0, []string{"quota_exhausted"})
	setQuota(store, "alpha", 10, "ok")
	sender.sendError = errors.New("temporary webhook failure")
	worker.Now = fixedNow("Asia/Shanghai", 2026, 7, 20, 10, 1, 0)
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("recovery send unexpectedly succeeded")
	}
	if _, found := store.notificationState(t).QuotaAlerts["alpha|default:primary_window"]; !found {
		t.Fatal("failed recovery cleared active alert")
	}
	sender.sendError = nil
	runWorkerAt(t, worker, 10, 2, []string{"quota_recovered"})
}

func TestWorkerScheduledReportAbsorbsTransitionsAndDisabledPreservesError(t *testing.T) {
	store, activity, sender := workerFixtures()
	store.settings["notification.daily_times"] = "09:01"
	worker := &Worker{Store: store, Activity: activity, Sender: sender}
	setQuota(store, "alpha", 40, "ok")
	runWorkerAt(t, worker, 9, 0, nil)
	setQuota(store, "alpha", 4, "ok")
	runWorkerAt(t, worker, 9, 1, []string{"scheduled"})
	if len(sender.contents) != 1 || !strings.Contains(sender.contents[0], "🔄 额度刷新") {
		t.Fatalf("scheduled transition content = %#v", sender.contents)
	}
	state := store.notificationState(t)
	state.LastError = "previous failure"
	store.writeNotificationState(t, state)
	store.settings["notification.enabled"] = false
	worker.Now = fixedNow("Asia/Shanghai", 2026, 7, 20, 10, 0, 0)
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Enabled || store.notificationState(t).LastError != "previous failure" {
		t.Fatalf("disabled run = (%#v, %v), state=%#v", result, err, store.notificationState(t))
	}
}

func testAccountSnapshot(account string, used float64, label string) AccountSnapshot {
	resetAt := int64(1_900_000_000)
	resetCount := int64(2)
	return AccountSnapshot{
		ID: account, ActiveUsers1H: 3,
		Quota: quota.AccountQuota{
			Account: account, Status: "ok", ResetCreditCount: &resetCount,
			WeeklyWindows: []quota.WeeklyWindow{{
				Key: "default:primary_window", Label: label, UsedPercent: used,
				LimitReached: used >= 100, ResetAt: &resetAt,
			}},
		},
	}
}

type fakeStore struct {
	mu       sync.Mutex
	settings map[string]any
	accounts []controlplane.Account
	runtime  map[string]json.RawMessage
}

func newFakeStore() *fakeStore {
	return &fakeStore{settings: make(map[string]any), runtime: make(map[string]json.RawMessage)}
}

func (store *fakeStore) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]controlplane.Account(nil), store.accounts...), nil
}

func (store *fakeStore) ReadSettings(context.Context) (map[string]any, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make(map[string]any, len(store.settings))
	for key, value := range store.settings {
		result[key] = value
	}
	return result, nil
}

func (store *fakeStore) ReadRuntimeState(_ context.Context, name string, destination any) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, found := store.runtime[name]
	if !found {
		return false, nil
	}
	return true, json.Unmarshal(raw, destination)
}

func (store *fakeStore) WriteRuntimeState(_ context.Context, name string, value any) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, err := json.Marshal(value)
	if err == nil {
		store.runtime[name] = raw
	}
	return err
}

func (store *fakeStore) PatchRuntimeState(_ context.Context, name string, changes map[string]any) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	payload := make(map[string]any)
	if raw, found := store.runtime[name]; found {
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
	}
	for key, value := range changes {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err == nil {
		store.runtime[name] = raw
	}
	return err
}

func (store *fakeStore) notificationState(t *testing.T) RuntimeState {
	t.Helper()
	state, _, err := ReadRuntimeState(context.Background(), store)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	return state
}

func (store *fakeStore) writeNotificationState(t *testing.T, state RuntimeState) {
	t.Helper()
	if err := store.WriteRuntimeState(context.Background(), RuntimeStateName, state); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}
}

type fakeActivity struct {
	mu     sync.Mutex
	values map[string]int
}

func (activity *fakeActivity) RefreshActiveUsersLastHour(context.Context) (map[string]int, error) {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	result := make(map[string]int, len(activity.values))
	for key, value := range activity.values {
		result[key] = value
	}
	return result, nil
}

type fakeSender struct {
	mu         sync.Mutex
	configured bool
	contents   []string
	sendError  error
}

func (sender *fakeSender) Configured(context.Context) (bool, error) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.configured, nil
}

func (sender *fakeSender) Send(_ context.Context, content string) (SendResult, error) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.sendError != nil {
		return SendResult{}, sender.sendError
	}
	sender.contents = append(sender.contents, content)
	return SendResult{ErrorCode: 0, Message: "ok"}, nil
}

func workerFixtures() (*fakeStore, *fakeActivity, *fakeSender) {
	store := newFakeStore()
	store.settings = map[string]any{
		"notification.enabled":                  true,
		"notification.timezone":                 "Asia/Shanghai",
		"notification.daily_times":              "09:00,14:00,18:00",
		"notification.schedule_grace_minutes":   float64(15),
		"notification.quota_alert_enabled":      true,
		"notification.weekly_threshold_percent": float64(90),
		"usage.quota_cache_seconds":             float64(60),
		"branding.short_name":                   "Codex CPA",
		"branding.public_base_url":              "https://cpa.example.com",
	}
	store.accounts = []controlplane.Account{{ID: "alpha"}}
	setQuota(store, "alpha", 25, "ok")
	return store, &fakeActivity{values: map[string]int{"alpha": 3}}, &fakeSender{configured: true}
}

func setQuota(store *fakeStore, account string, used float64, status string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	resetAt := int64(1_900_000_000)
	resetCount := int64(2)
	state := quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: account, Status: status, ResetCreditCount: &resetCount,
			WeeklyWindows: []quota.WeeklyWindow{{
				Key: "default:primary_window", Label: "常规周限额", UsedPercent: used,
				LimitReached: used >= 100, ResetAt: &resetAt,
			}},
		}}},
	}
	raw, _ := json.Marshal(state)
	store.runtime[quota.RuntimeStateName] = raw
}

func fixedNow(zone string, year int, month time.Month, day int, hour int, minute int, second int) func() time.Time {
	location, err := time.LoadLocation(zone)
	if err != nil {
		panic(err)
	}
	value := time.Date(year, month, day, hour, minute, second, 0, location)
	return func() time.Time { return value }
}

func runWorkerAt(t *testing.T, worker *Worker, hour int, minute int, expected []string) {
	t.Helper()
	worker.Now = fixedNow("Asia/Shanghai", 2026, 7, 20, hour, minute, 0)
	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce %02d:%02d: %v", hour, minute, err)
	}
	if strings.Join(result.Sent, ",") != strings.Join(expected, ",") {
		t.Fatalf("RunOnce %02d:%02d sent = %v, want %v", hour, minute, result.Sent, expected)
	}
}

type fakeSecretStore struct {
	value string
	found bool
	err   error
}

type notificationWriteFenceStub struct {
	calls int
	err   error
}

func (fence *notificationWriteFenceStub) WithWriteFence(
	_ context.Context,
	operation func() error,
) error {
	fence.calls++
	if fence.err != nil {
		return fence.err
	}
	return operation()
}

func (store *fakeSecretStore) ReadSecret(context.Context, string) (string, bool, error) {
	return store.value, store.found, store.err
}

type transportResponse struct {
	status int
	body   string
}

type recordingTransport struct {
	mu        sync.Mutex
	responses []transportResponse
	requests  []*http.Request
	bodies    [][]byte
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.requests = append(transport.requests, request.Clone(request.Context()))
	transport.bodies = append(transport.bodies, body)
	if len(transport.responses) == 0 {
		return nil, errors.New("unexpected request")
	}
	response := transport.responses[0]
	transport.responses = transport.responses[1:]
	return &http.Response{
		StatusCode: response.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Request:    request,
	}, nil
}
