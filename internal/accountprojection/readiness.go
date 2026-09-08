package accountprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

type UserKeyReadinessStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadInternalKey(context.Context, string) (controlplane.InternalKey, bool, error)
}

// UserKeyReadiness checks the newly created user's own internal credential on
// its assigned account, or an enabled account before first-login assignment.
// It never assigns a route. An old user's working Key cannot prove this reload.
type UserKeyReadiness struct {
	Store   UserKeyReadinessStore
	Client  *http.Client
	Timeout time.Duration
}

func (probe UserKeyReadiness) VerifyUserKey(ctx context.Context, user string) error {
	if probe.Store == nil {
		return errors.New("user Key readiness store is unavailable")
	}
	user = strings.ToLower(strings.TrimSpace(user))
	key, found, err := probe.Store.ReadInternalKey(ctx, user)
	if err != nil || !found || key.Status != "active" || strings.TrimSpace(key.Key) == "" || strings.ContainsAny(key.Key, "\r\n\x00") {
		return errors.New("user Key readiness requires an active internal credential")
	}
	routes, err := probe.Store.ReadRoutes(ctx)
	if err != nil {
		return errors.New("read user Key readiness route failed")
	}
	targets := []string{}
	if account := routes[user]; account != "" {
		targets = append(targets, account)
	} else {
		accounts, err := probe.Store.ReadAccounts(ctx)
		if err != nil {
			return errors.New("read user Key readiness accounts failed")
		}
		for _, account := range accounts {
			if account.GroupEnabled {
				targets = append(targets, account.ID)
			}
		}
	}
	if len(targets) == 0 {
		return errors.New("user Key readiness requires an enabled account")
	}
	for _, account := range targets {
		normalized, err := controlplane.NormalizeAccountID(account)
		if err != nil || normalized != account {
			return errors.New("user Key readiness requires a valid account")
		}
	}
	client := http.Client{}
	if probe.Client != nil {
		client = *probe.Client
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		defer transport.CloseIdleConnections()
		client.Transport = transport
	}
	// Credentials must never follow a redirect or use an external HTTP proxy.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	timeout := probe.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	status := 0
	for {
		for _, account := range targets {
			// A stopped or unresponsive candidate must not consume the entire budget
			// before another enabled account can authenticate an unassigned user.
			attempt, stop := context.WithTimeout(ctx, time.Second)
			request, err := http.NewRequestWithContext(attempt, http.MethodGet, "http://cliproxy-"+account+":8317/v1/models", nil)
			if err != nil {
				stop()
				return errors.New("construct user Key readiness request failed")
			}
			request.Header.Set("Authorization", "Bearer "+key.Key)
			response, requestErr := client.Do(request)
			if response != nil {
				status = response.StatusCode
				var models struct {
					Data []json.RawMessage `json:"data"`
				}
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
				_ = response.Body.Close()
				if requestErr == nil && readErr == nil && status == http.StatusOK && len(body) <= 1024*1024 &&
					json.Unmarshal(body, &models) == nil && models.Data != nil {
					stop()
					return nil
				}
			}
			stop()
			if ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			// Do not attach transport errors or upstream bodies: either can echo a Key.
			return fmt.Errorf("account did not activate the user credential (HTTP %d): %w", status, ctx.Err())
		case <-ticker.C:
		}
	}
}
