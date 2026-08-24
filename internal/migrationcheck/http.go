package migrationcheck

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

const (
	maximumResponseBytes = 4 * 1024 * 1024
	maximumStreamBytes   = int64(1024 * 1024)
	maximumStreamEvents  = 12
)

type Config struct {
	V1PublicURL      string
	V2PublicURL      string
	V1InternalURL    string
	V2InternalURL    string
	TestKey          string
	Timeout          time.Duration
	AllowNonLoopback bool
	StreamBody       []byte
}

type Runner struct {
	v1Public   string
	v2Public   string
	v1Internal string
	v2Internal string
	testKey    string
	timeout    time.Duration
	streamBody []byte
	client     *resty.Client
}

type Report struct {
	Passed bool          `json:"passed"`
	Checks []CheckResult `json:"checks"`
}

type CheckResult struct {
	Name   string      `json:"name"`
	Passed bool        `json:"passed"`
	Reason string      `json:"reason,omitempty"`
	V1     Observation `json:"v1"`
	V2     Observation `json:"v2"`
}

type Observation struct {
	Status           int      `json:"status,omitempty"`
	ContentType      string   `json:"content_type,omitempty"`
	ErrorType        string   `json:"error_type,omitempty"`
	ErrorCode        string   `json:"error_code,omitempty"`
	PayloadSHA256    string   `json:"payload_sha256,omitempty"`
	ItemCount        int      `json:"item_count,omitempty"`
	AuthGeneration   string   `json:"auth_generation,omitempty"`
	QuotaGeneration  string   `json:"quota_generation,omitempty"`
	StreamEventTypes []string `json:"stream_event_types,omitempty"`
	FailureType      string   `json:"failure_type,omitempty"`
}

type responseObservation struct {
	Observation
	body []byte
}

func New(config Config) (*Runner, error) {
	if config.Timeout <= 0 {
		return nil, errors.New("comparison timeout must be positive")
	}
	testKey := strings.TrimSpace(config.TestKey)
	if testKey == "" || len(testKey) > 4096 || strings.ContainsAny(testKey, " \t\r\n") {
		return nil, errors.New("dedicated test Key is invalid")
	}
	v1Public, err := normalizeOrigin("v1 public", config.V1PublicURL, config.AllowNonLoopback)
	if err != nil {
		return nil, err
	}
	v2Public, err := normalizeOrigin("v2 public", config.V2PublicURL, config.AllowNonLoopback)
	if err != nil {
		return nil, err
	}
	if v1Public == v2Public {
		return nil, errors.New("v1 and v2 public origins must differ")
	}
	v1Internal, v2Internal := "", ""
	if strings.TrimSpace(config.V1InternalURL) != "" || strings.TrimSpace(config.V2InternalURL) != "" {
		if strings.TrimSpace(config.V1InternalURL) == "" || strings.TrimSpace(config.V2InternalURL) == "" {
			return nil, errors.New("v1 and v2 internal origins must be provided together")
		}
		v1Internal, err = normalizeOrigin("v1 internal", config.V1InternalURL, config.AllowNonLoopback)
		if err != nil {
			return nil, err
		}
		v2Internal, err = normalizeOrigin("v2 internal", config.V2InternalURL, config.AllowNonLoopback)
		if err != nil {
			return nil, err
		}
		if v1Internal == v2Internal {
			return nil, errors.New("v1 and v2 internal origins must differ")
		}
	}
	client := resty.New().
		SetTimeout(config.Timeout).
		SetRetryCount(0).
		SetResponseBodyLimit(maximumResponseBytes).
		SetRedirectPolicy(resty.NoRedirectPolicy())
	return &Runner{
		v1Public: v1Public, v2Public: v2Public,
		v1Internal: v1Internal, v2Internal: v2Internal,
		testKey: testKey, timeout: config.Timeout,
		streamBody: append([]byte(nil), config.StreamBody...),
		client:     client,
	}, nil
}

func (runner *Runner) Run(ctx context.Context) Report {
	checks := []CheckResult{
		runner.compareSimple(ctx, "public_health", "/__health", "", func(left, right *responseObservation) (bool, string) {
			passed := left.Status == http.StatusOK && right.Status == http.StatusOK &&
				strings.TrimSpace(string(left.body)) == "ok" && strings.TrimSpace(string(right.body)) == "ok"
			return passed, reasonUnless(passed, "both versions must return 200 ok")
		}),
		runner.compareSimple(ctx, "blocked_public_path", "/v0/management", "Bearer "+runner.testKey, func(left, right *responseObservation) (bool, string) {
			passed := left.Status == http.StatusNotFound && right.Status == http.StatusNotFound
			return passed, reasonUnless(passed, "both versions must block unknown public paths with 404")
		}),
		runner.compareSimple(ctx, "invalid_api_key", "/v1/models", "Bearer cpa_migration_invalid_probe", compareInvalidKey),
		runner.compareSimple(ctx, "dedicated_test_key_models", "/v1/models", "Bearer "+runner.testKey, compareModels),
	}
	if runner.v1Internal != "" {
		checks = append(checks, runner.compareSnapshots(ctx))
	}
	if len(runner.streamBody) > 0 {
		checks = append(checks, runner.compareStream(ctx))
	}
	report := Report{Passed: true, Checks: checks}
	for _, check := range checks {
		if !check.Passed {
			report.Passed = false
			break
		}
	}
	return report
}

func (runner *Runner) compareSimple(
	ctx context.Context,
	name string,
	path string,
	authorization string,
	compare func(*responseObservation, *responseObservation) (bool, string),
) CheckResult {
	v1 := runner.get(ctx, runner.v1Public+path, authorization)
	v2 := runner.get(ctx, runner.v2Public+path, authorization)
	passed, reason := compare(&v1, &v2)
	if v1.FailureType != "" || v2.FailureType != "" {
		passed = false
		reason = "one or both version requests failed"
	}
	return CheckResult{Name: name, Passed: passed, Reason: reason, V1: v1.Observation, V2: v2.Observation}
}

func (runner *Runner) get(ctx context.Context, target string, authorization string) responseObservation {
	request := runner.client.R().SetContext(ctx)
	if authorization != "" {
		request.SetHeader("Authorization", authorization)
	}
	response, err := request.Get(target)
	if err != nil {
		return responseObservation{Observation: Observation{FailureType: fmt.Sprintf("%T", err)}}
	}
	return observeResponse(response)
}

func observeResponse(response *resty.Response) responseObservation {
	body := append([]byte(nil), response.Body()...)
	observation := responseObservation{
		Observation: Observation{
			Status:      response.StatusCode(),
			ContentType: normalizedContentType(response.Header().Get("Content-Type")),
		},
		body: body,
	}
	decodeErrorObservation(body, &observation.Observation)
	return observation
}

func decodeErrorObservation(body []byte, observation *Observation) {
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		observation.ErrorType = envelope.Error.Type
		observation.ErrorCode = envelope.Error.Code
	}
}

func compareInvalidKey(left, right *responseObservation) (bool, string) {
	passed := left.Status == http.StatusUnauthorized && right.Status == http.StatusUnauthorized &&
		left.ErrorType == "invalid_request_error" && left.ErrorType == right.ErrorType &&
		left.ErrorCode == right.ErrorCode
	return passed, reasonUnless(passed, "invalid Key status and error contract must match")
}

func compareModels(left, right *responseObservation) (bool, string) {
	leftDigest, leftCount, leftOK := canonicalModelDigest(left.body)
	rightDigest, rightCount, rightOK := canonicalModelDigest(right.body)
	left.PayloadSHA256, left.ItemCount = leftDigest, leftCount
	right.PayloadSHA256, right.ItemCount = rightDigest, rightCount
	passed := left.Status == http.StatusOK && right.Status == http.StatusOK && leftOK && rightOK &&
		leftDigest == rightDigest
	return passed, reasonUnless(passed, "dedicated test Key must resolve to the same successful model catalog")
}

func canonicalModelDigest(raw []byte) (string, int, bool) {
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Data == nil {
		return "", 0, false
	}
	canonical, err := json.Marshal(payload.Data)
	if err != nil {
		return "", 0, false
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), len(payload.Data), true
}

func (runner *Runner) compareSnapshots(ctx context.Context) CheckResult {
	v1 := runner.get(ctx, runner.v1Internal+"/__internal/snapshots", "")
	v2 := runner.get(ctx, runner.v2Internal+"/__internal/snapshots", "")
	decodeSnapshotObservation(&v1)
	decodeSnapshotObservation(&v2)
	passed := v1.FailureType == "" && v2.FailureType == "" &&
		v1.Status == http.StatusOK && v2.Status == http.StatusOK &&
		v1.AuthGeneration != "" && v2.AuthGeneration != "" &&
		v1.QuotaGeneration != "" && v2.QuotaGeneration != ""
	return CheckResult{
		Name: "active_snapshot_generations", Passed: passed,
		Reason: reasonUnless(passed, "both versions must report active auth and quota generations"),
		V1:     v1.Observation, V2: v2.Observation,
	}
}

func decodeSnapshotObservation(observation *responseObservation) {
	var payload struct {
		Auth struct {
			ActiveGeneration string `json:"active_generation"`
		} `json:"auth"`
		Quota struct {
			ActiveGeneration string `json:"active_generation"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(observation.body, &payload); err != nil {
		return
	}
	observation.AuthGeneration = payload.Auth.ActiveGeneration
	observation.QuotaGeneration = payload.Quota.ActiveGeneration
}

func (runner *Runner) compareStream(ctx context.Context) CheckResult {
	v1 := runner.stream(ctx, runner.v1Public+"/v1/responses")
	v2 := runner.stream(ctx, runner.v2Public+"/v1/responses")
	passed := v1.FailureType == "" && v2.FailureType == "" &&
		v1.Status == http.StatusOK && v2.Status == http.StatusOK &&
		v1.ContentType == "text/event-stream" && v2.ContentType == "text/event-stream" &&
		len(v1.StreamEventTypes) >= 2 && strings.Join(v1.StreamEventTypes, "\x00") == strings.Join(v2.StreamEventTypes, "\x00")
	return CheckResult{
		Name: "codex_responses_stream", Passed: passed,
		Reason: reasonUnless(passed, "both versions must expose the same initial SSE event sequence"),
		V1:     v1, V2: v2,
	}
}

func (runner *Runner) stream(ctx context.Context, target string) Observation {
	streamContext, cancel := context.WithTimeout(ctx, runner.timeout)
	defer cancel()
	response, err := runner.client.R().
		SetContext(streamContext).
		SetHeader("Authorization", "Bearer "+runner.testKey).
		SetHeader("Content-Type", "application/json").
		SetBody(runner.streamBody).
		SetDoNotParseResponse(true).
		Post(target)
	if err != nil {
		return Observation{FailureType: fmt.Sprintf("%T", err)}
	}
	if response.RawBody() == nil {
		return Observation{Status: response.StatusCode(), FailureType: "missing_response_body"}
	}
	defer response.RawBody().Close()
	observation := Observation{
		Status:      response.StatusCode(),
		ContentType: normalizedContentType(response.Header().Get("Content-Type")),
	}
	if observation.Status != http.StatusOK || observation.ContentType != "text/event-stream" {
		body, readError := io.ReadAll(io.LimitReader(response.RawBody(), maximumResponseBytes+1))
		if readError != nil {
			observation.FailureType = fmt.Sprintf("%T", readError)
			return observation
		}
		if len(body) > maximumResponseBytes {
			observation.FailureType = "response_body_limit_exceeded"
			return observation
		}
		decodeErrorObservation(body, &observation)
		return observation
	}
	events, err := readSSEEventTypes(response.RawBody(), maximumStreamBytes, maximumStreamEvents)
	if err != nil {
		observation.FailureType = fmt.Sprintf("%T", err)
		return observation
	}
	observation.StreamEventTypes = events
	return observation
}

func readSSEEventTypes(reader io.Reader, maximumBytes int64, maximumEvents int) ([]string, error) {
	limited := &io.LimitedReader{R: reader, N: maximumBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	events := make([]string, 0, maximumEvents)
	eventName, data := "", ""
	flush := func() {
		if eventName == "" && data == "" {
			return
		}
		kind := eventName
		if kind == "" {
			if strings.TrimSpace(data) == "[DONE]" {
				kind = "[DONE]"
			} else {
				var payload struct {
					Type string `json:"type"`
				}
				if json.Unmarshal([]byte(data), &payload) == nil && payload.Type != "" {
					kind = payload.Type
				} else {
					kind = "data"
				}
			}
		}
		events = append(events, kind)
		eventName, data = "", ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			if len(events) >= maximumEvents || (len(events) >= 2 && events[len(events)-1] == "[DONE]") {
				return events, nil
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			part := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				data = part
			} else {
				data += "\n" + part
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	if limited.N <= 0 {
		return nil, errors.New("SSE comparison exceeded the response limit")
	}
	return events, nil
}

func normalizeOrigin(name string, raw string, allowNonLoopback bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s URL must be an origin-only HTTP(S) URL", name)
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	loopback := strings.EqualFold(hostname, "localhost")
	if address := net.ParseIP(hostname); address != nil {
		loopback = address.IsLoopback()
	}
	if !allowNonLoopback && !loopback {
		return "", fmt.Errorf("%s URL must use a literal loopback host unless non-loopback Test targets are explicitly allowed", name)
	}
	parsed.Path = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizedContentType(value string) string {
	if separator := strings.IndexByte(value, ';'); separator >= 0 {
		value = value[:separator]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func reasonUnless(passed bool, reason string) string {
	if passed {
		return ""
	}
	return reason
}
