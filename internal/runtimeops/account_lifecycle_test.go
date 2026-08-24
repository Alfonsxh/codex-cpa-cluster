package runtimeops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/containerd/errdefs"
	"github.com/go-resty/resty/v2"
	containertypes "github.com/moby/moby/api/types/container"
	networktypes "github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

func TestAccountRuntimeCreatesExactMobySpecProbesAndRollsBackCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{containers: runtimeFixtureContainers()}
	client.containers[0].Ports = []containertypes.PortSummary{{PrivatePort: 8317, PublicPort: 18318, Type: "tcp"}}
	manager := newRuntimeTestManager(t, client)
	probe := &recordingRoundTripper{status: http.StatusOK}
	runtime := newAccountRuntimeForTest(t, manager, root, probe)
	ports, err := runtime.ReservedHostPorts(context.Background())
	if err != nil {
		t.Fatalf("ReservedHostPorts: %v", err)
	}
	if _, found := ports[18318]; !found {
		t.Fatalf("reserved ports = %#v", ports)
	}
	transition, err := runtime.PrepareCreate(context.Background(), controlplane.Account{
		ID: "gamma", Port: 18320, GroupEnabled: true,
	})
	if err != nil {
		t.Fatalf("PrepareCreate: %v", err)
	}
	if len(client.creates) != 1 {
		t.Fatalf("create calls = %d", len(client.creates))
	}
	created := client.creates[0]
	if created.Name != "fixture-gamma" || created.Config.Image != "registry.example.com/cpa@sha256:immutable" ||
		created.Config.Labels[composeProjectLabel] != "fixture-project" ||
		created.Config.Labels[composeServiceLabel] != "cliproxy-gamma" ||
		created.HostConfig.NetworkMode != "fixture-backend" || len(created.HostConfig.Mounts) != 4 {
		t.Fatalf("created options = %#v", created)
	}
	port := created.HostConfig.PortBindings[networkPort(t, accountContainerPort)]
	if len(port) != 1 || port[0].HostPort != "18320" || port[0].HostIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("created port binding = %#v", port)
	}
	if probe.authorization != "Bearer cpa_internal_fixture" ||
		probe.url != "http://cliproxy-gamma:8317/v1/models" {
		t.Fatalf("probe = auth %q url %q", probe.authorization, probe.url)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("removed candidates = %#v", client.removed)
	}
}

func TestAccountRuntimeUpdateRollbackRecreatesPreviousContainer(t *testing.T) {
	root := newRuntimeRoot(t, "alpha", "gamma")
	client := &fakeAccountLifecycleClient{containers: []containertypes.Summary{
		accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
	}}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	transition, err := runtime.PrepareUpdate(context.Background(),
		controlplane.Account{ID: "alpha", Port: 18318},
		controlplane.Account{ID: "gamma", Port: 18318},
	)
	if err != nil {
		t.Fatalf("PrepareUpdate: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" ||
		len(client.creates) != 1 || client.creates[0].Name != "fixture-gamma" {
		t.Fatalf("prepared update: removed=%#v creates=%#v", client.removed, client.creates)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(client.creates) != 2 || client.creates[1].Name != "fixture-alpha" ||
		client.creates[1].Config.Labels[composeServiceLabel] != "cliproxy-alpha" {
		t.Fatalf("restored create options = %#v", client.creates)
	}
}

func TestAccountRuntimeDeleteDefersRemovalUntilCommitAndCanRollbackAfterCommit(t *testing.T) {
	root := newRuntimeRoot(t, "alpha")
	client := &fakeAccountLifecycleClient{containers: []containertypes.Summary{
		accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
	}}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	transition, err := runtime.PrepareDelete(context.Background(), controlplane.Account{ID: "alpha", Port: 18318})
	if err != nil {
		t.Fatalf("PrepareDelete: %v", err)
	}
	if len(client.removed) != 0 {
		t.Fatalf("delete removed container before snapshot commit: %#v", client.removed)
	}
	if err := transition.Commit(context.Background()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" {
		t.Fatalf("delete commit removed = %#v", client.removed)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback after commit: %v", err)
	}
	if len(client.creates) != 1 || client.creates[0].Name != "fixture-alpha" {
		t.Fatalf("delete rollback did not recreate previous account: %#v", client.creates)
	}
}

func TestAccountRuntimeProbeFailureRemovesFailedCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusServiceUnavailable})
	runtime.probeTimeout = time.Second
	if _, err := runtime.PrepareCreate(context.Background(), controlplane.Account{ID: "gamma", Port: 18320}); err == nil {
		t.Fatal("PrepareCreate unexpectedly accepted a failed model probe")
	}
	if len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("failed candidate removals = %#v", client.removed)
	}
}

func TestAccountRuntimeReconcileReplacesInterruptedRenameCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "alpha", "gamma")
	client := &fakeAccountLifecycleClient{containers: []containertypes.Summary{
		accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
	}}
	runtime := newAccountRuntimeForTest(
		t,
		newRuntimeTestManager(t, client),
		root,
		&recordingRoundTripper{status: http.StatusOK},
	)
	if err := runtime.ReconcileAccount(
		context.Background(),
		"alpha",
		&controlplane.Account{ID: "gamma", Port: 18318},
	); err != nil {
		t.Fatalf("ReconcileAccount: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" ||
		len(client.creates) != 1 || client.creates[0].Name != "fixture-gamma" ||
		len(client.started) != 1 {
		t.Fatalf("reconcile calls: removed=%#v creates=%#v started=%#v", client.removed, client.creates, client.started)
	}
}

func newAccountRuntimeForTest(
	t *testing.T,
	manager *Manager,
	root string,
	transport http.RoundTripper,
) *AccountRuntime {
	t.Helper()
	store := &fakeAccountRuntimeStore{
		settings: map[string]any{"accounts.listen_address": "127.0.0.1"},
		imageState: map[string]any{"applied": map[string]any{
			"resolved_ref": "registry.example.com/cpa@sha256:immutable",
		}},
		keys: map[string]controlplane.InternalKey{
			"alice@example.com": {Key: "cpa_internal_fixture", Status: "active"},
		},
	}
	runtime, err := NewAccountRuntime(manager, store, AccountRuntimeConfig{
		Root: root, NetworkName: "fixture-backend", InstanceName: "fixture",
		ProbeTimeout: time.Second,
		HTTPClient:   resty.NewWithClient(&http.Client{Transport: transport, Timeout: time.Second}),
	})
	if err != nil {
		t.Fatalf("NewAccountRuntime: %v", err)
	}
	return runtime
}

func newRuntimeRoot(t *testing.T, accounts ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, account := range accounts {
		for path, directory := range map[string]bool{
			filepath.Join(root, "configs", account+".yaml"): false,
			filepath.Join(root, "auth", account):            true,
			filepath.Join(root, "logs", account):            true,
		} {
			if directory {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatalf("create runtime directory: %v", err)
				}
			} else {
				writeRuntimeTestFile(t, path, "host: \"\"\nport: 8317\n")
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "management", "config", "static"), 0o700); err != nil {
		t.Fatalf("create management static directory: %v", err)
	}
	return root
}

func accountContainerSummary(id, account string, state containertypes.ContainerState, publicPort uint16) containertypes.Summary {
	return containertypes.Summary{
		ID: id, Names: []string{"/fixture-" + account}, Image: "fixture/cpa:v1",
		State: state, Status: string(state), Ports: []containertypes.PortSummary{{
			PrivatePort: 8317, PublicPort: publicPort, Type: "tcp",
		}},
		Labels: map[string]string{
			composeProjectLabel: "fixture-project", composeServiceLabel: "cliproxy-" + account,
		},
	}
}

type fakeAccountLifecycleClient struct {
	containers  []containertypes.Summary
	creates     []dockerclient.ContainerCreateOptions
	started     []string
	stopped     []string
	restarted   []string
	removed     []string
	createCount int
}

func (client *fakeAccountLifecycleClient) ContainerList(
	_ context.Context,
	_ dockerclient.ContainerListOptions,
) (dockerclient.ContainerListResult, error) {
	return dockerclient.ContainerListResult{Items: append([]containertypes.Summary(nil), client.containers...)}, nil
}

func (client *fakeAccountLifecycleClient) ContainerCreate(
	_ context.Context,
	options dockerclient.ContainerCreateOptions,
) (dockerclient.ContainerCreateResult, error) {
	client.createCount++
	id := "created-" + strconv.Itoa(client.createCount)
	client.creates = append(client.creates, options)
	client.containers = append(client.containers, containertypes.Summary{
		ID: id, Names: []string{"/" + options.Name}, Image: options.Config.Image,
		State: containertypes.StateCreated, Status: "created", Labels: options.Config.Labels,
	})
	return dockerclient.ContainerCreateResult{ID: id}, nil
}

func (client *fakeAccountLifecycleClient) ContainerInspect(
	_ context.Context,
	id string,
	_ dockerclient.ContainerInspectOptions,
) (dockerclient.ContainerInspectResult, error) {
	for _, candidate := range client.containers {
		if candidate.ID == id {
			return dockerclient.ContainerInspectResult{Container: containertypes.InspectResponse{
				ID: id, State: &containertypes.State{Running: candidate.State == containertypes.StateRunning},
			}}, nil
		}
	}
	return dockerclient.ContainerInspectResult{}, fmt.Errorf("%w: fixture container", errdefs.ErrNotFound)
}

func (client *fakeAccountLifecycleClient) ContainerStart(
	_ context.Context,
	id string,
	_ dockerclient.ContainerStartOptions,
) (dockerclient.ContainerStartResult, error) {
	client.started = append(client.started, id)
	client.setState(id, containertypes.StateRunning)
	return dockerclient.ContainerStartResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerStop(
	_ context.Context,
	id string,
	_ dockerclient.ContainerStopOptions,
) (dockerclient.ContainerStopResult, error) {
	client.stopped = append(client.stopped, id)
	client.setState(id, containertypes.StateExited)
	return dockerclient.ContainerStopResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerRestart(
	_ context.Context,
	id string,
	_ dockerclient.ContainerRestartOptions,
) (dockerclient.ContainerRestartResult, error) {
	client.restarted = append(client.restarted, id)
	client.setState(id, containertypes.StateRunning)
	return dockerclient.ContainerRestartResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerRemove(
	_ context.Context,
	id string,
	_ dockerclient.ContainerRemoveOptions,
) (dockerclient.ContainerRemoveResult, error) {
	client.removed = append(client.removed, id)
	filtered := client.containers[:0]
	for _, candidate := range client.containers {
		if candidate.ID != id {
			filtered = append(filtered, candidate)
		}
	}
	client.containers = filtered
	return dockerclient.ContainerRemoveResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerLogs(
	context.Context,
	string,
	dockerclient.ContainerLogsOptions,
) (dockerclient.ContainerLogsResult, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (client *fakeAccountLifecycleClient) setState(id string, state containertypes.ContainerState) {
	for index := range client.containers {
		if client.containers[index].ID == id {
			client.containers[index].State = state
			client.containers[index].Status = string(state)
		}
	}
}

type fakeAccountRuntimeStore struct {
	settings   map[string]any
	imageState map[string]any
	keys       map[string]controlplane.InternalKey
}

func (store *fakeAccountRuntimeStore) ReadSettings(context.Context) (map[string]any, error) {
	return store.settings, nil
}

func (store *fakeAccountRuntimeStore) ReadRuntimeState(_ context.Context, _ string, target any) (bool, error) {
	raw, _ := json.Marshal(store.imageState)
	return true, json.Unmarshal(raw, target)
}

func (store *fakeAccountRuntimeStore) ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error) {
	return store.keys, nil
}

type recordingRoundTripper struct {
	status        int
	authorization string
	url           string
}

func (transport *recordingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.authorization = request.Header.Get("Authorization")
	transport.url = request.URL.String()
	return &http.Response{
		StatusCode: transport.status,
		Status:     strconv.Itoa(transport.status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    request,
	}, nil
}

func networkPort(t *testing.T, value string) networktypes.Port {
	t.Helper()
	port, err := networktypes.ParsePort(value)
	if err != nil {
		t.Fatalf("parse network port: %v", err)
	}
	return port
}

func writeRuntimeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create runtime test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write runtime test file: %v", err)
	}
}
