package runtimeops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

const (
	diagnosticModelsPath    = "/__internal/probe/models"
	diagnosticResponseLimit = 1024 * 1024
)

var diagnosticAccountIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

type DiagnosticStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error)
}

type DiagnosticProjection interface {
	Render(context.Context) (accountprojection.Result, error)
}

type DiagnosticHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// DiagnosticController preserves the three established operations exposed by
// the Admin page. Every method writes only sanitized operational text;
// raw API Keys are used solely in the outbound Authorization header.
type DiagnosticController interface {
	Health(context.Context, io.Writer) (OperationResult, error)
	VerifyRouting(context.Context, io.Writer) (OperationResult, error)
	Render(context.Context, io.Writer) (OperationResult, error)
}

type DiagnosticsConfig struct {
	Root             string
	Store            DiagnosticStore
	Projection       DiagnosticProjection
	GatewayProbeURLs []string
	HTTPClient       DiagnosticHTTPClient
	RenderEnabled    bool
}

type Diagnostics struct {
	root          string
	store         DiagnosticStore
	projection    DiagnosticProjection
	probeURLs     []string
	client        DiagnosticHTTPClient
	renderEnabled bool
}

func NewDiagnostics(config DiagnosticsConfig) (*Diagnostics, error) {
	if config.Store == nil {
		return nil, errors.New("runtime diagnostics require a control-plane store")
	}
	if config.Projection == nil {
		return nil, errors.New("runtime diagnostics require an account projection renderer")
	}
	root := strings.TrimSpace(config.Root)
	if root == "" {
		return nil, errors.New("runtime diagnostics require a deployment root")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime diagnostics root: %w", err)
	}
	probeURLs := make([]string, 0, len(config.GatewayProbeURLs))
	seen := make(map[string]struct{}, len(config.GatewayProbeURLs))
	for _, raw := range config.GatewayProbeURLs {
		normalized, err := normalizeDiagnosticProbeURL(raw)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		probeURLs = append(probeURLs, normalized)
	}
	if len(probeURLs) == 0 {
		return nil, errors.New("runtime diagnostics require at least one Gateway probe URL")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Diagnostics{
		root: absoluteRoot, store: config.Store, projection: config.Projection,
		probeURLs: probeURLs, client: client, renderEnabled: config.RenderEnabled,
	}, nil
}

func normalizeDiagnosticProbeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid Gateway diagnostic probe URL %q", raw)
	}
	parsed.Path = strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (diagnostics *Diagnostics) Health(ctx context.Context, output io.Writer) (OperationResult, error) {
	accounts, err := diagnostics.store.ReadAccounts(ctx)
	if err != nil {
		return OperationResult{}, fmt.Errorf("read health account catalog: %w", err)
	}
	records, err := diagnostics.store.ReadKeyRecords(ctx)
	if err != nil {
		return OperationResult{}, fmt.Errorf("read health Key catalog: %w", err)
	}
	routes, err := diagnostics.store.ReadRoutes(ctx)
	if err != nil {
		return OperationResult{}, fmt.Errorf("read health routes: %w", err)
	}
	byAccount := diagnosticHealthRecords(records, routes)
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return OperationResult{}, err
		}
		record, found := byAccount[account.ID]
		if !found {
			_, _ = fmt.Fprintf(output, "%s %s NO_KEY\n", account.ID, account.Email)
			continue
		}
		authFiles := diagnostics.authFileCount(account.ID)
		status, models, probeError := diagnostics.probeModels(ctx, record.Key)
		if probeError != nil {
			if ctx.Err() != nil {
				return OperationResult{}, ctx.Err()
			}
			_, _ = fmt.Fprintf(output, "%s %s ERROR %s\n", account.ID, account.Email, Sanitize(probeError.Error()))
			continue
		}
		_, _ = fmt.Fprintf(
			output,
			"%s %s HTTP %d AUTH_FILES %d MODELS %d\n",
			account.ID,
			account.Email,
			status,
			authFiles,
			models,
		)
	}
	return OperationResult{Action: "health", Target: "all"}, nil
}

func diagnosticHealthRecords(
	records []controlplane.KeyRecord,
	routes map[string]string,
) map[string]controlplane.KeyRecord {
	byUser := make(map[string][]controlplane.KeyRecord)
	userOrder := make([]string, 0)
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.Status), "active") {
			if _, exists := byUser[record.User]; !exists {
				userOrder = append(userOrder, record.User)
			}
			byUser[record.User] = append(byUser[record.User], record)
		}
	}
	byAccount := make(map[string]controlplane.KeyRecord)
	for _, user := range userOrder {
		active := byUser[user]
		keys := make(map[string]struct{}, len(active))
		for _, record := range active {
			keys[record.Key] = struct{}{}
		}
		if len(keys) == 1 {
			route := routes[user]
			for _, record := range active {
				if record.Account == route {
					if _, exists := byAccount[route]; !exists {
						byAccount[route] = record
					}
					break
				}
			}
			continue
		}
		for _, record := range active {
			if _, exists := byAccount[record.Account]; !exists {
				byAccount[record.Account] = record
			}
		}
	}
	return byAccount
}

func (diagnostics *Diagnostics) VerifyRouting(ctx context.Context, output io.Writer) (OperationResult, error) {
	records, err := diagnostics.store.ReadKeyRecords(ctx)
	if err != nil {
		return OperationResult{}, fmt.Errorf("read routing Key catalog: %w", err)
	}
	_, _ = fmt.Fprintln(output, "LABEL\tACCOUNT\tHTTP")
	for _, record := range records {
		if !strings.EqualFold(strings.TrimSpace(record.Status), "active") {
			continue
		}
		status := 0
		for _, probeURL := range diagnostics.probeURLs {
			status, err = diagnostics.probeStatus(ctx, probeURL, record.Key)
			if ctx.Err() != nil {
				return OperationResult{}, ctx.Err()
			}
			if err == nil && status != http.StatusForbidden && status != http.StatusNotFound {
				break
			}
		}
		_, _ = fmt.Fprintf(output, "%s\t%s\t%d\n", record.Label, record.Account, status)
	}
	return OperationResult{Action: "verify-routing", Target: "all"}, nil
}

func (diagnostics *Diagnostics) Render(ctx context.Context, _ io.Writer) (OperationResult, error) {
	if !diagnostics.renderEnabled {
		return OperationResult{}, ErrRuntimeReadOnly
	}
	if _, err := diagnostics.projection.Render(ctx); err != nil {
		return OperationResult{}, fmt.Errorf("render and validate account projection: %w", err)
	}
	return OperationResult{Action: "render", Target: "all"}, nil
}

func (diagnostics *Diagnostics) authFileCount(accountID string) int {
	if !diagnosticAccountIDPattern.MatchString(accountID) {
		return 0
	}
	entries, err := os.ReadDir(filepath.Join(diagnostics.root, "auth", accountID))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			count++
		}
	}
	return count
}

func (diagnostics *Diagnostics) probeModels(ctx context.Context, key string) (int, int, error) {
	var lastError error
	status := 0
	for _, probeURL := range diagnostics.probeURLs {
		responseStatus, payload, err := diagnostics.probe(ctx, probeURL, key)
		status = responseStatus
		if err != nil {
			lastError = err
			continue
		}
		if status == http.StatusForbidden || status == http.StatusNotFound {
			continue
		}
		if status < 200 || status >= 300 {
			return status, 0, nil
		}
		var decoded struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return status, 0, fmt.Errorf("decode Gateway model probe: %w", err)
		}
		return status, len(decoded.Data), nil
	}
	if lastError != nil {
		return status, 0, lastError
	}
	return status, 0, nil
}

func (diagnostics *Diagnostics) probeStatus(ctx context.Context, probeURL string, key string) (int, error) {
	status, _, err := diagnostics.probe(ctx, probeURL, key)
	return status, err
}

func (diagnostics *Diagnostics) probe(ctx context.Context, baseURL string, key string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+diagnosticModelsPath, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("build Gateway model probe: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := diagnostics.client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("request Gateway model probe: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, diagnosticResponseLimit+1))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("read Gateway model probe: %w", err)
	}
	if len(payload) > diagnosticResponseLimit {
		return response.StatusCode, nil, errors.New("Gateway model probe response exceeds 1 MiB")
	}
	return response.StatusCode, payload, nil
}
