package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
)

// SaveImage exports an image already present in the host Docker daemon's
// store as a tar stream, implementing internal/deploy.ImageSaver — used by
// the cloud_vm SSH deployer to pipe the image into "docker load" on a remote
// VM without a registry round-trip. The caller must Close the returned reader.
func (m *Manager) SaveImage(ctx context.Context, ref string) (io.ReadCloser, error) {
	reader, err := m.client.ImageSave(ctx, []string{ref})
	if err != nil {
		return nil, fmt.Errorf("docker: save image %s: %w", ref, err)
	}
	return reader, nil
}

// PushImage pushes an image already present in the host Docker daemon's
// store to its registry, implementing internal/deploy.ImagePusher. RegistryAuth
// is required by the daemon API even for unauthenticated registries, so when
// auth is nil an empty AuthConfig is base64-encoded and sent — the local
// registry has no auth configured. A non-nil auth is used for authenticated
// pushes to ECR/ACR.
func (m *Manager) PushImage(ctx context.Context, ref string, auth *registry.AuthConfig) error {
	if auth == nil {
		auth = &registry.AuthConfig{}
	}
	authJSON, err := json.Marshal(auth)
	if err != nil {
		return fmt.Errorf("docker: encode push auth: %w", err)
	}

	reader, err := m.client.ImagePush(ctx, ref, image.PushOptions{
		RegistryAuth: base64.URLEncoding.EncodeToString(authJSON),
	})
	if err != nil {
		return fmt.Errorf("docker: push image %s: %w", ref, err)
	}
	defer reader.Close()

	dec := json.NewDecoder(reader)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("docker: read push response for %s: %w", ref, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("docker: push %s: %s", ref, msg.Error)
		}
	}
}

// TagImage applies an additional tag to an image already present in the host
// Docker daemon's store, implementing internal/deploy.ImagePusher — used to
// retag a locally-built image for an ECR/ACR repository before pushing.
func (m *Manager) TagImage(ctx context.Context, source, target string) error {
	if err := m.client.ImageTag(ctx, source, target); err != nil {
		return fmt.Errorf("docker: tag image %s as %s: %w", source, target, err)
	}
	return nil
}
