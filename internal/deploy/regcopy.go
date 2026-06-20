package deploy

import (
	"fmt"
	"io"

	"github.com/docker/docker/api/types/registry"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
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

// saveImageFromRegistry pulls an image from the local insecure registry via
// crane (bypassing the Docker daemon entirely) and returns a standard Docker
// tar stream suitable for `docker load` on a remote VM. This avoids the
// containerd image-store bug where `docker save` produces corrupt tars.
func saveImageFromRegistry(src, tag string) (io.ReadCloser, error) {
	img, err := crane.Pull(src, crane.Insecure)
	if err != nil {
		return nil, fmt.Errorf("deploy: crane pull %s: %w", src, err)
	}

	tagRef, err := name.NewTag(tag, name.Insecure)
	if err != nil {
		return nil, fmt.Errorf("deploy: parse tag %s: %w", tag, err)
	}

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(tarball.Write(tagRef, img, pw))
	}()
	return pr, nil
}

// ensure v1 import is used
var _ v1.Image
