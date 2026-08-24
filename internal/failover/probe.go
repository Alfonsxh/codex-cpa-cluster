package failover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"golang.org/x/sync/errgroup"
)

type TCPRuntimeProbe struct {
	Address        func(controlplane.Account) string
	DialTimeout    time.Duration
	MaxConcurrency int
	DialContext    func(context.Context, string, string) (net.Conn, error)
}

func (probe TCPRuntimeProbe) ProbeAccounts(
	ctx context.Context,
	accounts []controlplane.Account,
) (map[string]bool, error) {
	result := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		result[account.ID] = false
	}
	if len(accounts) == 0 {
		return result, nil
	}
	timeout := probe.DialTimeout
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = time.Second
	}
	dialContext := probe.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: timeout, KeepAlive: -1}
		dialContext = dialer.DialContext
	}
	limit := probe.MaxConcurrency
	if limit <= 0 || limit > 32 {
		limit = 8
	}
	var mutex sync.Mutex
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(limit)
	for _, account := range accounts {
		account := account
		group.Go(func() error {
			address := defaultAccountProbeAddress(account)
			if probe.Address != nil {
				address = strings.TrimSpace(probe.Address(account))
			}
			if address == "" {
				return nil
			}
			dialCtx, cancel := context.WithTimeout(groupContext, timeout)
			defer cancel()
			connection, err := dialContext(dialCtx, "tcp", address)
			if err != nil {
				if groupContext.Err() != nil &&
					(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
					return groupContext.Err()
				}
				return nil
			}
			_ = connection.Close()
			mutex.Lock()
			result[account.ID] = true
			mutex.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return result, fmt.Errorf("run TCP account probes: %w", err)
	}
	return result, nil
}

func defaultAccountProbeAddress(account controlplane.Account) string {
	return net.JoinHostPort("cliproxy-"+account.ID, strconv.Itoa(8317))
}
