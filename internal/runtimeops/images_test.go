package runtimeops

import (
	"context"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	imagetypes "github.com/moby/moby/api/types/image"
	dockerclient "github.com/moby/moby/client"
)

func TestCPAImageStatusMatchesTargetAndAccountContainers(t *testing.T) {
	containers := runtimeFixtureContainers()
	containers[0].ImageID = "sha256:target"
	containers[1].ImageID = "sha256:old"
	client := &fakeImageDocker{
		fakeDockerClient: &fakeDockerClient{containers: containers},
		inspect:          dockerclient.ImageInspectResult{},
		images: dockerclient.ImageListResult{Items: []imagetypes.Summary{{
			RepoTags: []string{"cpa-cpa-rollback:beta"},
		}}},
	}
	client.inspect.ID = "sha256:target"
	client.inspect.Created = "2026-08-21T00:00:00Z"
	client.inspect.RepoDigests = []string{"example/cpa@sha256:digest"}
	store := &fakeImageStore{
		accounts: []controlplane.Account{{ID: "alpha", GroupEnabled: true}, {ID: "beta", GroupEnabled: true}},
		settings: map[string]any{"runtime.cliproxy_image": "example/cpa:v2"},
		state: map[string]any{
			"candidate": map[string]any{
				"image_id": "sha256:target", "version": "v2.0.0", "commit": "abcdef",
				"built_at": "2026-08-21T00:00:00Z", "resolved_ref": "example/cpa:v2@sha256:digest",
			},
			"applied": map[string]any{"image_id": "sha256:old", "version": "v1.0.0"},
		},
	}
	manager, err := New(client, "fixture-project", store)
	if err != nil {
		t.Fatalf("New image runtime: %v", err)
	}
	status, err := manager.CPAImageStatus(context.Background())
	if err != nil {
		t.Fatalf("CPAImageStatus: %v", err)
	}
	if status.TargetImage != "example/cpa:v2" || !status.LocalImage.Available ||
		status.LocalImage.Version != "v2.0.0" || status.RunningCount != 2 ||
		status.CurrentCount != 1 || status.OutdatedCount != 1 || len(status.Accounts) != 2 ||
		!status.Accounts[0].UsingTarget || status.Accounts[1].UsingTarget ||
		!status.Accounts[1].RollbackAvailable || status.Accounts[1].Version != "v1.0.0" {
		t.Fatalf("CPA image status = %#v", status)
	}
}

type fakeImageStore struct {
	accounts []controlplane.Account
	settings map[string]any
	state    map[string]any
}

func (store *fakeImageStore) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account{}, store.accounts...), nil
}

func (store *fakeImageStore) ReadSettings(context.Context) (map[string]any, error) {
	return store.settings, nil
}

func (store *fakeImageStore) ReadRuntimeState(_ context.Context, _ string, target any) (bool, error) {
	output := target.(*map[string]any)
	*output = store.state
	return true, nil
}

type fakeImageDocker struct {
	*fakeDockerClient
	inspect dockerclient.ImageInspectResult
	images  dockerclient.ImageListResult
}

func (client *fakeImageDocker) ImageInspect(
	context.Context,
	string,
	...dockerclient.ImageInspectOption,
) (dockerclient.ImageInspectResult, error) {
	return client.inspect, nil
}

func (client *fakeImageDocker) ImageList(
	context.Context,
	dockerclient.ImageListOptions,
) (dockerclient.ImageListResult, error) {
	return client.images, nil
}
