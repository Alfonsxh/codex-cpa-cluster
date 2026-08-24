package runtimeops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/containerd/errdefs"
	containertypes "github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

const defaultCPAImage = "docker.m.daocloud.io/eceasy/cli-proxy-api:v7.1.23"

type imageRuntimeStore interface {
	ReadSettings(context.Context) (map[string]any, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
}

type imageDockerClient interface {
	ImageInspect(context.Context, string, ...dockerclient.ImageInspectOption) (dockerclient.ImageInspectResult, error)
	ImageList(context.Context, dockerclient.ImageListOptions) (dockerclient.ImageListResult, error)
}

type CPAImageLocal struct {
	Available   bool     `json:"available"`
	ID          string   `json:"id"`
	ShortID     string   `json:"short_id"`
	Created     string   `json:"created"`
	RepoDigests []string `json:"repo_digests"`
	Version     string   `json:"version"`
	Commit      string   `json:"commit"`
	BuiltAt     string   `json:"built_at"`
	ResolvedRef string   `json:"resolved_ref"`
}

type CPAAccountImage struct {
	Account           string `json:"account"`
	Service           string `json:"service"`
	Enabled           bool   `json:"enabled"`
	ContainerExists   bool   `json:"container_exists"`
	Running           bool   `json:"running"`
	State             string `json:"state"`
	ImageRef          string `json:"image_ref"`
	ImageID           string `json:"image_id"`
	ImageShortID      string `json:"image_short_id"`
	Version           string `json:"version"`
	UsingTarget       bool   `json:"using_target"`
	RollbackAvailable bool   `json:"rollback_available"`
}

type CPAImageStatus struct {
	TargetImage   string            `json:"target_image"`
	UpdateChannel string            `json:"update_channel"`
	Candidate     map[string]any    `json:"candidate"`
	Applied       map[string]any    `json:"applied"`
	LocalImage    CPAImageLocal     `json:"local_image"`
	Accounts      []CPAAccountImage `json:"accounts"`
	RunningCount  int               `json:"running_count"`
	CurrentCount  int               `json:"current_count"`
	OutdatedCount int               `json:"outdated_count"`
}

func (manager *Manager) CPAImageStatus(ctx context.Context) (CPAImageStatus, error) {
	store, ok := manager.accounts.(imageRuntimeStore)
	if !ok {
		return CPAImageStatus{}, fmt.Errorf("runtime account catalog does not expose image state")
	}
	docker, ok := manager.client.(imageDockerClient)
	if !ok {
		return CPAImageStatus{}, fmt.Errorf("Docker client does not expose image inspection")
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("read CPA image settings: %w", err)
	}
	target := strings.TrimSpace(stringValue(settings["runtime.cliproxy_image"]))
	if target == "" {
		target = defaultCPAImage
	}
	state := make(map[string]any)
	if _, err := store.ReadRuntimeState(ctx, "cliproxy_image", &state); err != nil {
		return CPAImageStatus{}, fmt.Errorf("read CPA image runtime state: %w", err)
	}
	result := CPAImageStatus{
		TargetImage: target, UpdateChannel: target,
		Candidate: mapValue(state["candidate"]), Applied: mapValue(state["applied"]),
		Accounts: make([]CPAAccountImage, 0),
	}
	inspected, inspectErr := docker.ImageInspect(ctx, target)
	if inspectErr == nil {
		result.LocalImage = CPAImageLocal{
			Available: true, ID: inspected.ID, ShortID: shortID(inspected.ID),
			Created: inspected.Created, RepoDigests: append([]string{}, inspected.RepoDigests...),
		}
		if stringValue(result.Candidate["image_id"]) != inspected.ID {
			result.Candidate = map[string]any{}
		}
		result.LocalImage.Version = stringValue(result.Candidate["version"])
		result.LocalImage.Commit = stringValue(result.Candidate["commit"])
		result.LocalImage.BuiltAt = stringValue(result.Candidate["built_at"])
		result.LocalImage.ResolvedRef = stringValue(result.Candidate["resolved_ref"])
	} else if !errdefs.IsNotFound(inspectErr) {
		return CPAImageStatus{}, fmt.Errorf("inspect target CPA image: %w", inspectErr)
	} else {
		result.Candidate = map[string]any{}
		result.LocalImage.RepoDigests = []string{}
	}
	images, err := docker.ImageList(ctx, dockerclient.ImageListOptions{})
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("list local CPA images: %w", err)
	}
	availableRefs := make(map[string]struct{})
	for _, image := range images.Items {
		for _, reference := range image.RepoTags {
			availableRefs[reference] = struct{}{}
		}
	}
	accounts, err := manager.accounts.ReadAccounts(ctx)
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("read CPA image accounts: %w", err)
	}
	filter := make(dockerclient.Filters).Add("label", composeProjectLabel+"="+manager.project)
	containers, err := manager.client.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("list CPA image containers: %w", err)
	}
	byService := make(map[string]containertypes.Summary)
	for _, container := range containers.Items {
		if container.Labels[composeProjectLabel] != manager.project {
			continue
		}
		service := strings.TrimSpace(container.Labels[composeServiceLabel])
		if strings.HasPrefix(service, "cliproxy-") {
			byService[service] = container
		}
	}
	for _, account := range accounts {
		service := "cliproxy-" + account.ID
		container, exists := byService[service]
		stateName := "missing"
		if exists {
			stateName = strings.ToLower(strings.TrimSpace(string(container.State)))
		}
		running := exists && stateName == "running"
		identity := knownImageIdentity(state, container.ImageID)
		rollbackReference := rollbackReference(container.Names, account.ID)
		_, rollbackAvailable := availableRefs[rollbackReference]
		item := CPAAccountImage{
			Account: account.ID, Service: service, Enabled: account.GroupEnabled,
			ContainerExists: exists, Running: running, State: stateName,
			ImageRef: container.Image, ImageID: container.ImageID, ImageShortID: shortID(container.ImageID),
			Version:           stringValue(identity["version"]),
			UsingTarget:       result.LocalImage.Available && container.ImageID == result.LocalImage.ID,
			RollbackAvailable: rollbackAvailable,
		}
		result.Accounts = append(result.Accounts, item)
		if account.GroupEnabled && running {
			result.RunningCount++
			if item.UsingTarget {
				result.CurrentCount++
			} else {
				result.OutdatedCount++
			}
		}
	}
	sort.Slice(result.Accounts, func(left, right int) bool {
		return result.Accounts[left].Account < result.Accounts[right].Account
	})
	return result, nil
}

func knownImageIdentity(state map[string]any, imageID string) map[string]any {
	if imageID == "" {
		return map[string]any{}
	}
	for _, name := range []string{"applied", "candidate"} {
		record := mapValue(state[name])
		if stringValue(record["image_id"]) == imageID {
			return record
		}
	}
	return mapValue(mapValue(state["history"])[imageID])
}

func rollbackReference(names []string, account string) string {
	for _, raw := range names {
		name := strings.TrimPrefix(raw, "/")
		suffix := "-" + account
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix) + "-cpa-rollback:" + account
		}
	}
	return ""
}

func mapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok && typed != nil {
		return typed
	}
	return map[string]any{}
}

func stringValue(value any) string {
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}
