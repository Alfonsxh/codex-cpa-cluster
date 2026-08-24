package runtimeops

import (
	"context"
	"errors"
	"fmt"
	"time"

	dockerclient "github.com/moby/moby/client"
)

const releaseMetadataTimeout = 30 * time.Second

type releaseDockerClient interface {
	ImagePull(context.Context, string, dockerclient.ImagePullOptions) (dockerclient.ImagePullResponse, error)
	ImageInspect(context.Context, string, ...dockerclient.ImageInspectOption) (dockerclient.ImageInspectResult, error)
}

func (manager *Manager) PullReleaseMetadata(ctx context.Context, image string) (map[string]string, error) {
	client, ok := manager.client.(releaseDockerClient)
	if !ok {
		return nil, errors.New("Docker client does not expose release image operations")
	}
	pullContext, cancel := context.WithTimeout(ctx, releaseMetadataTimeout)
	defer cancel()
	stream, err := client.ImagePull(pullContext, image, dockerclient.ImagePullOptions{})
	if err != nil {
		return nil, fmt.Errorf("pull release metadata image: %w", err)
	}
	defer stream.Close()
	if err := stream.Wait(pullContext); err != nil {
		return nil, fmt.Errorf("wait for release metadata image: %w", err)
	}
	inspected, err := client.ImageInspect(pullContext, image)
	if err != nil {
		return nil, fmt.Errorf("inspect release metadata image: %w", err)
	}
	if inspected.Config == nil || inspected.Config.Labels == nil {
		return nil, errors.New("release metadata image has no labels")
	}
	labels := make(map[string]string, len(inspected.Config.Labels))
	for key, value := range inspected.Config.Labels {
		labels[key] = value
	}
	return labels, nil
}
