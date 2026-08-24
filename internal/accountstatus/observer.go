package accountstatus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	ReasonCredentialUnavailable = "credential_unavailable"
	ReasonDegraded              = "degraded"
	ReasonQuotaExhausted        = "quota_exhausted"
	ReasonRateLimited           = "rate_limited"
	ReasonRuntimeUnknown        = "runtime_unknown"
	ReasonTransientCooldown     = "transient_cooldown"

	managementSecretName = "cpa_management_key"
	managementBodyLimit  = int64(2 * 1024 * 1024)
	managementWorkers    = 16
	errorWindow          = time.Hour
	activeErrorWindow    = 15 * time.Minute
	defaultCacheTTL      = 15 * time.Second
	defaultProbeTimeout  = 3 * time.Second
	maximumAccessLine    = 64 * 1024
)

// SecretReader is intentionally narrower than the control-plane store. The
// observer needs the CPA management credential only while refreshing the
// bounded native-status snapshot and never retains or returns it.
type SecretReader interface {
	ReadSecret(context.Context, string) (string, bool, error)
}

// State is the privacy-bounded runtime overlay consumed by the Admin's
// account-state provider. It does not contain account email, request labels,
// management responses, status messages, or credentials.
type State struct {
	Reason             string
	DisableEligibility bool
	Exhausted          bool
}

type Config struct {
	Root         string
	Secrets      SecretReader
	Transport    http.RoundTripper
	Now          func() time.Time
	CacheTTL     time.Duration
	ProbeTimeout time.Duration
}

type Observer struct {
	secrets      SecretReader
	client       *http.Client
	now          func() time.Time
	cacheTTL     time.Duration
	probeTimeout time.Duration
	access       *accessLogReader

	mu               sync.Mutex
	cacheFingerprint string
	cacheExpiresAt   time.Time
	cache            map[string]State
	refresh          singleflight.Group
}

func New(config Config) (*Observer, error) {
	root := filepath.Clean(strings.TrimSpace(config.Root))
	if root == "" || !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, errors.New("account status observer requires an absolute deployment root")
	}
	if config.Secrets == nil {
		return nil, errors.New("account status observer requires a secret reader")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = defaultCacheTTL
	}
	if config.ProbeTimeout <= 0 {
		config.ProbeTimeout = defaultProbeTimeout
	}
	transport := config.Transport
	if transport == nil {
		cloned := http.DefaultTransport.(*http.Transport).Clone()
		// Management credentials must go directly to the account container on
		// the private Docker network, never through an environment proxy.
		cloned.Proxy = nil
		transport = cloned
	}
	return &Observer{
		secrets: config.Secrets,
		client: &http.Client{
			Transport: transport,
			Timeout:   config.ProbeTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: config.Now, cacheTTL: config.CacheTTL, probeTimeout: config.ProbeTimeout,
		access: newAccessLogReader(filepath.Join(root, "logs", "gateway", "access.tsv")),
	}, nil
}

// Observe returns one best-effort runtime overlay for each requested account.
// Native failures degrade to runtime_unknown and a missing access log means no
// recent error activity; neither condition can turn a hard blocker healthy.
func (observer *Observer) Observe(ctx context.Context, accountServices map[string]string) map[string]State {
	fingerprint := serviceFingerprint(accountServices)
	now := observer.now()
	observer.mu.Lock()
	if fingerprint == observer.cacheFingerprint && now.Before(observer.cacheExpiresAt) {
		cached := cloneStates(observer.cache)
		observer.mu.Unlock()
		return cached
	}
	observer.mu.Unlock()

	resultChannel := observer.refresh.DoChan(fingerprint, func() (any, error) {
		refreshContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), observer.probeTimeout+time.Second)
		defer cancel()
		refreshed := observer.refreshStates(refreshContext, accountServices, observer.now())
		observer.mu.Lock()
		observer.cacheFingerprint = fingerprint
		observer.cacheExpiresAt = observer.now().Add(observer.cacheTTL)
		observer.cache = cloneStates(refreshed)
		observer.mu.Unlock()
		return refreshed, nil
	})
	select {
	case <-ctx.Done():
		return unknownStates(accountServices)
	case result := <-resultChannel:
		if result.Err != nil {
			return unknownStates(accountServices)
		}
		states, _ := result.Val.(map[string]State)
		return cloneStates(states)
	}
}

func (observer *Observer) refreshStates(
	ctx context.Context,
	accountServices map[string]string,
	now time.Time,
) map[string]State {
	activity := observer.access.recent(now)
	native := observer.nativeSnapshots(ctx, accountServices)
	states := make(map[string]State, len(accountServices))
	for account := range accountServices {
		snapshot, found := native[account]
		if !found {
			snapshot = nativeSnapshot{queryStatus: "unavailable", credentialStatus: "unknown"}
		}
		states[account] = presentRuntime(snapshot, activity[account], now)
	}
	return states
}

type authFile struct {
	Unavailable   bool   `json:"unavailable"`
	Disabled      bool   `json:"disabled"`
	Status        string `json:"status"`
	StatusMessage string `json:"status_message"`
}

type authFilePayload struct {
	Files []authFile `json:"files"`
}

type nativeSnapshot struct {
	queryStatus           string
	credentialStatus      string
	credentialUnavailable bool
	credentialDisabled    bool
	statusMessage         string
}

func (observer *Observer) nativeSnapshots(
	ctx context.Context,
	accountServices map[string]string,
) map[string]nativeSnapshot {
	result := make(map[string]nativeSnapshot, len(accountServices))
	for account := range accountServices {
		result[account] = nativeSnapshot{queryStatus: "unavailable", credentialStatus: "unknown"}
	}
	managementKey, found, err := observer.secrets.ReadSecret(ctx, managementSecretName)
	managementKey = strings.TrimSpace(managementKey)
	if err != nil || !found || managementKey == "" || strings.ContainsAny(managementKey, "\r\n\x00") {
		return result
	}
	var mu sync.Mutex
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(managementWorkers)
	for account, service := range accountServices {
		account, service := account, service
		group.Go(func() error {
			snapshot := observer.probeNative(groupContext, account, service, managementKey)
			mu.Lock()
			result[account] = snapshot
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	return result
}

func (observer *Observer) probeNative(
	ctx context.Context,
	account string,
	service string,
	managementKey string,
) nativeSnapshot {
	fallback := nativeSnapshot{queryStatus: "unavailable", credentialStatus: "unknown"}
	if !validService(account, service) {
		return fallback
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://"+service+":8317/v0/management/auth-files",
		nil,
	)
	if err != nil {
		return fallback
	}
	request.Header.Set("Authorization", "Bearer "+managementKey)
	request.Header.Set("Accept", "application/json")
	response, err := observer.client.Do(request)
	if err != nil {
		return fallback
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32*1024))
		return fallback
	}
	limited := io.LimitReader(response.Body, managementBodyLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil || int64(len(raw)) > managementBodyLimit {
		return fallback
	}
	var payload authFilePayload
	if json.Unmarshal(raw, &payload) != nil {
		return fallback
	}
	snapshot := nativeSnapshot{queryStatus: "ok", credentialStatus: "missing"}
	if len(payload.Files) == 0 {
		return snapshot
	}
	unavailable := 0
	disabled := 0
	degraded := false
	for _, file := range payload.Files {
		if file.Unavailable || file.Disabled {
			unavailable++
		}
		if file.Disabled {
			disabled++
		}
		if strings.TrimSpace(file.StatusMessage) != "" && snapshot.statusMessage == "" {
			snapshot.statusMessage = strings.TrimSpace(file.StatusMessage)
		}
		status := strings.ToLower(strings.TrimSpace(file.Status))
		if status != "" && status != "active" {
			degraded = true
		}
	}
	snapshot.credentialUnavailable = unavailable == len(payload.Files)
	snapshot.credentialDisabled = disabled == len(payload.Files)
	switch {
	case snapshot.credentialUnavailable:
		snapshot.credentialStatus = "unavailable"
	case unavailable > 0 || degraded:
		snapshot.credentialStatus = "degraded"
	default:
		snapshot.credentialStatus = "active"
	}
	return snapshot
}

func presentRuntime(snapshot nativeSnapshot, activity accessActivity, now time.Time) State {
	state := "unknown"
	lastErrorAge := time.Duration(-1)
	if !activity.lastErrorAt.IsZero() {
		lastErrorAge = now.Sub(activity.lastErrorAt)
		if lastErrorAge < 0 {
			lastErrorAge = 0
		}
	}
	switch {
	case snapshot.credentialUnavailable ||
		(snapshot.queryStatus == "ok" && snapshot.credentialStatus == "missing"):
		state = "unavailable"
	case activity.rate429 > 0 && lastErrorAge >= 0 && lastErrorAge <= activeErrorWindow:
		state = "rate_limited"
	case activity.errors > 0:
		state = "degraded"
	case snapshot.credentialStatus == "degraded":
		state = "degraded"
	case snapshot.credentialStatus == "active":
		state = "healthy"
	}
	switch state {
	case "unavailable":
		switch {
		case unavailableDueToQuota(snapshot.statusMessage):
			return State{Reason: ReasonQuotaExhausted, DisableEligibility: true, Exhausted: true}
		case snapshot.credentialStatus != "missing" && !snapshot.credentialDisabled &&
			!unavailableDueToInvalidCredential(snapshot.statusMessage) &&
			unavailableDueToTransientError(snapshot.statusMessage):
			return State{Reason: ReasonTransientCooldown}
		default:
			return State{Reason: ReasonCredentialUnavailable, DisableEligibility: true}
		}
	case "rate_limited":
		return State{Reason: ReasonRateLimited}
	case "degraded":
		return State{Reason: ReasonDegraded}
	case "unknown":
		return State{Reason: ReasonRuntimeUnknown}
	default:
		return State{}
	}
}

func unavailableDueToQuota(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(message), &payload) == nil {
		return strings.EqualFold(strings.TrimSpace(payload.Error.Type), "usage_limit_reached") ||
			strings.EqualFold(strings.TrimSpace(payload.Error.Code), "usage_limit_reached")
	}
	return strings.Contains(strings.ToLower(message), "usage_limit_reached")
}

func unavailableDueToInvalidCredential(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	markers := []string{
		"invalid_grant", "refresh_token_invalidated", "refresh_token_expired",
		"invalid_refresh_token", "invalid_token", "invalid_api_key",
		"authentication_error", "authentication_required", "oauth_token_expired",
		"access_denied", "unauthorized", "forbidden",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return strings.Contains(message, "refresh token") &&
		(strings.Contains(message, "invalid") || strings.Contains(message, "expired") || strings.Contains(message, "revoked"))
}

func unavailableDueToTransientError(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	markers := []string{
		"service_unavailable", "server_error", "internal_server_error", "bad_gateway",
		"gateway_timeout", "request_timeout", "upstream_error", "connection reset",
		"connection refused", "connection timed out", `"status":408`, `"status": 408`,
		`"status":500`, `"status": 500`, `"status":502`, `"status": 502`,
		`"status":503`, `"status": 503`, `"status":504`, `"status": 504`,
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func validService(account string, service string) bool {
	if len(account) < 2 || len(account) > 32 || account[0] < 'a' || account[0] > 'z' ||
		service != "cliproxy-"+account {
		return false
	}
	for _, character := range account[1:] {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func serviceFingerprint(services map[string]string) string {
	accounts := make([]string, 0, len(services))
	for account := range services {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	digest := sha256.New()
	for _, account := range accounts {
		_, _ = fmt.Fprintf(digest, "%s\x00%s\n", account, services[account])
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func cloneStates(source map[string]State) map[string]State {
	result := make(map[string]State, len(source))
	for account, state := range source {
		result[account] = state
	}
	return result
}

func unknownStates(services map[string]string) map[string]State {
	result := make(map[string]State, len(services))
	for account := range services {
		result[account] = State{Reason: ReasonRuntimeUnknown}
	}
	return result
}

type accessLogRow struct {
	timestamp time.Time
	account   string
	status    int
}

type accessActivity struct {
	errors      int
	rate429     int
	lastErrorAt time.Time
}

type accessLogReader struct {
	path string

	mu       sync.Mutex
	identity os.FileInfo
	offset   int64
	prefix   []byte
	pending  []byte
	dropping bool
	rows     []accessLogRow
	lastNow  time.Time
}

func newAccessLogReader(path string) *accessLogReader {
	return &accessLogReader{path: path}
}

func (reader *accessLogReader) recent(now time.Time) map[string]accessActivity {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if !reader.lastNow.IsZero() && now.Before(reader.lastNow) {
		reader.reset()
	}
	reader.readAppends()
	cutoff := now.Add(-errorWindow)
	retained := reader.rows[:0]
	result := make(map[string]accessActivity)
	for _, row := range reader.rows {
		if row.timestamp.Before(cutoff) {
			continue
		}
		retained = append(retained, row)
		if row.timestamp.After(now.Add(time.Second)) || !isOperationalError(row.status) {
			continue
		}
		activity := result[row.account]
		activity.errors++
		if row.status == http.StatusTooManyRequests {
			activity.rate429++
		}
		if row.timestamp.After(activity.lastErrorAt) {
			activity.lastErrorAt = row.timestamp
		}
		result[row.account] = activity
	}
	reader.rows = retained
	reader.lastNow = now
	return result
}

func (reader *accessLogReader) readAppends() {
	information, err := os.Lstat(reader.path)
	if err != nil || information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		reader.reset()
		return
	}
	file, err := os.Open(reader.path)
	if err != nil {
		reader.reset()
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		reader.reset()
		return
	}
	changed := reader.identity == nil || !os.SameFile(reader.identity, stat) || stat.Size() < reader.offset
	if !changed && reader.offset > 0 && len(reader.prefix) > 0 {
		current := make([]byte, min(int64(len(reader.prefix)), reader.offset))
		if _, err := file.ReadAt(current, 0); err != nil && err != io.EOF {
			changed = true
		} else if !bytes.Equal(current, reader.prefix[:len(current)]) {
			changed = true
		}
	}
	if changed {
		reader.reset()
		reader.identity = stat
	}
	if _, err := file.Seek(reader.offset, io.SeekStart); err != nil {
		reader.reset()
		return
	}
	buffer := make([]byte, 64*1024)
	for {
		count, readError := file.Read(buffer)
		if count > 0 {
			reader.offset += int64(count)
			reader.consume(buffer[:count])
		}
		if readError != nil {
			break
		}
	}
	prefixLength := min(reader.offset, int64(256))
	reader.prefix = make([]byte, prefixLength)
	if prefixLength > 0 {
		_, _ = file.ReadAt(reader.prefix, 0)
	}
}

func (reader *accessLogReader) consume(chunk []byte) {
	for len(chunk) > 0 {
		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if reader.dropping {
				return
			}
			reader.pending = append(reader.pending, chunk...)
			if len(reader.pending) > maximumAccessLine {
				reader.pending = nil
				reader.dropping = true
			}
			return
		}
		part := chunk[:newline]
		chunk = chunk[newline+1:]
		if reader.dropping {
			reader.dropping = false
			reader.pending = nil
			continue
		}
		reader.pending = append(reader.pending, part...)
		if len(reader.pending) <= maximumAccessLine {
			if row, ok := parseAccessLogLine(reader.pending); ok {
				reader.rows = append(reader.rows, row)
			}
		}
		reader.pending = nil
	}
}

func (reader *accessLogReader) reset() {
	reader.identity = nil
	reader.offset = 0
	reader.prefix = nil
	reader.pending = nil
	reader.dropping = false
	reader.rows = nil
}

func parseAccessLogLine(line []byte) (accessLogRow, bool) {
	fields := bytes.Split(bytes.TrimSuffix(line, []byte{'\r'}), []byte{'\t'})
	if len(fields) < 4 {
		return accessLogRow{}, false
	}
	timestamp, err := strconv.ParseFloat(string(fields[0]), 64)
	if err != nil {
		return accessLogRow{}, false
	}
	status, err := strconv.Atoi(string(fields[3]))
	if err != nil {
		return accessLogRow{}, false
	}
	account := strings.TrimSpace(string(fields[2]))
	if account == "" {
		return accessLogRow{}, false
	}
	seconds, fraction := mathModf(timestamp)
	return accessLogRow{
		timestamp: time.Unix(seconds, int64(fraction*float64(time.Second))),
		account:   account,
		status:    status,
	}, true
}

func mathModf(value float64) (int64, float64) {
	seconds := int64(value)
	return seconds, value - float64(seconds)
}

func isOperationalError(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}
