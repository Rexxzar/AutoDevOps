package deploy

import (
	"fmt"

	"github.com/docker/docker/api/types/registry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
)

// copyImage copies an image directly between two registries without going
// through the Docker daemon's local store — avoids the containerd image-store
// bug where pull→tag→push loses layer content digests.
func copyImage(src, dst string, auth *registry.AuthConfig) error {
	srcOpts := []crane.Option{crane.Insecure}
	dstOpts := []crane.Option{}
	if auth != nil {
		dstOpts = append(dstOpts, crane.WithAuth(authn.FromConfig(authn.AuthConfig{
			Username: auth.Username,
			Password: auth.Password,
		})))
	}

	srcRef, err := name.ParseReference(src, name.Insecure)
	if err != nil {
		return fmt.Errorf("deploy: parse source ref %s: %w", src, err)
	}

	img, err := crane.Pull(src, srcOpts...)
	if err != nil {
		return fmt.Errorf("deploy: pull from local registry %s: %w", src, err)
	}

	dstRef, err := name.ParseReference(dst)
	if err != nil {
		return fmt.Errorf("deploy: parse dest ref %s: %w", dst, err)
	}

	_ = srcRef // used only for error messages above
	if err := crane.Push(img, dstRef.String(), dstOpts...); err != nil {
		return fmt.Errorf("deploy: push to cloud registry %s: %w", dst, err)
	}

	return nil
}
