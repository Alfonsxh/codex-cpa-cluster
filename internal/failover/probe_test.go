package failover

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestTCPRuntimeProbeFailsClosedPerAccount(t *testing.T) {
	probe := TCPRuntimeProbe{
		DialTimeout: time.Second, MaxConcurrency: 2,
		Address: func(account controlplane.Account) string { return account.ID + ":8317" },
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			if address != "alpha:8317" {
				return nil, errors.New("connection refused")
			}
			client, server := net.Pipe()
			_ = server.Close()
			return client, nil
		},
	}
	result, err := probe.ProbeAccounts(context.Background(), []controlplane.Account{
		{ID: "alpha"}, {ID: "beta"},
	})
	if err != nil || !result["alpha"] || result["beta"] {
		t.Fatalf("ProbeAccounts = (%#v, %v)", result, err)
	}
}

func TestTCPRuntimeProbePropagatesRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := TCPRuntimeProbe{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return nil, ctx.Err()
		},
	}
	_, err := probe.ProbeAccounts(ctx, []controlplane.Account{{ID: "alpha"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe error = %v", err)
	}
}

func TestDefaultAccountProbeAddressUsesDockerServiceNetwork(t *testing.T) {
	if got := defaultAccountProbeAddress(controlplane.Account{ID: "alpha"}); got != "cliproxy-alpha:8317" {
		t.Fatalf("defaultAccountProbeAddress = %q", got)
	}
}
