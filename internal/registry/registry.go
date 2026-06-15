// Package registry provides read-only access to the local OCI image registry
// (the kind/Docker setup's plain-HTTP registry, addressed via
// pkg/config.Config.RegistryPush, e.g. "localhost:5001") via
// go-containerregistry — used by the Image Registry dashboard page to list
// built images without shelling out to docker or crane.
package registry

import (
	"context"
	"fmt"
	"path"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Build methods reported in ImageInfo.BuiltVia.
const (
	BuiltViaPaketo = "paketo"
	BuiltViaDocker = "docker"
)

// paketoLabel is set by the Paketo lifecycle on every image it produces — its
// presence distinguishes Paketo-built images from Dockerfile-built ones
// (internal/deploy's "Dockerfile-if-present, else Paketo" dispatch) for the
// registry UI's badge.
const paketoLabel = "io.buildpacks.lifecycle.metadata"

// ImageInfo describes one tag of one repository in the local registry.
type ImageInfo struct {
	Repo      string    `json:"repo"`
	Tag       string    `json:"tag"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	BuiltVia  string    `json:"built_via"` // "paketo" | "docker"
}

// ListImages returns every (repo, tag) pair in the registry at registryHost
// (e.g. "localhost:5001"), with size/created/build-method metadata read from
// each image's OCI config. registryHost is treated as plain HTTP, matching
// the local registry's setup.
func ListImages(ctx context.Context, registryHost string) ([]ImageInfo, error) {
	reg, err := name.NewRegistry(registryHost, name.Insecure)
	if err != nil {
		return nil, fmt.Errorf("registry: parse host %s: %w", registryHost, err)
	}

	repoNames, err := remote.Catalog(ctx, reg)
	if err != nil {
		return nil, fmt.Errorf("registry: list catalog at %s: %w", registryHost, err)
	}

	var images []ImageInfo
	for _, repoName := range repoNames {
		repo, err := name.NewRepository(fmt.Sprintf("%s/%s", registryHost, repoName), name.Insecure)
		if err != nil {
			return nil, fmt.Errorf("registry: parse repo %s: %w", repoName, err)
		}

		tags, err := remote.List(repo, remote.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("registry: list tags for %s: %w", repoName, err)
		}

		for _, tag := range tags {
			ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", registryHost, repoName, tag), name.Insecure)
			if err != nil {
				return nil, fmt.Errorf("registry: parse ref %s:%s: %w", repoName, tag, err)
			}

			img, err := remote.Image(ref, remote.WithContext(ctx))
			if err != nil {
				return nil, fmt.Errorf("registry: read image %s:%s: %w", repoName, tag, err)
			}

			cfg, err := img.ConfigFile()
			if err != nil {
				return nil, fmt.Errorf("registry: read config for %s:%s: %w", repoName, tag, err)
			}

			size, err := img.Size()
			if err != nil {
				return nil, fmt.Errorf("registry: read size for %s:%s: %w", repoName, tag, err)
			}

			builtVia := BuiltViaDocker
			if _, ok := cfg.Config.Labels[paketoLabel]; ok {
				builtVia = BuiltViaPaketo
			}

			images = append(images, ImageInfo{
				Repo:      repoName,
				Tag:       tag,
				SizeBytes: size,
				CreatedAt: cfg.Created.Time,
				BuiltVia:  builtVia,
			})
		}
	}

	return images, nil
}

// DeleteImage removes repo:tag from the registry at registryHost. The
// registry HTTP API only supports deleting manifests by digest (registry:2
// returns 405 for a DELETE-by-tag), so this resolves the tag to its digest
// first. Deleting the manifest invalidates every other tag that happens to
// share the same digest — the same behaviour as `docker rmi` followed by GC.
func DeleteImage(ctx context.Context, registryHost, repo, tag string) error {
	tagRef, err := name.NewTag(fmt.Sprintf("%s/%s:%s", registryHost, repo, tag), name.Insecure)
	if err != nil {
		return fmt.Errorf("registry: parse ref %s:%s: %w", repo, tag, err)
	}

	desc, err := remote.Head(tagRef, remote.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("registry: resolve digest for %s:%s: %w", repo, tag, err)
	}

	digestRef, err := name.NewDigest(fmt.Sprintf("%s/%s@%s", registryHost, repo, desc.Digest.String()), name.Insecure)
	if err != nil {
		return fmt.Errorf("registry: build digest ref for %s:%s: %w", repo, tag, err)
	}

	if err := remote.Delete(digestRef, remote.WithContext(ctx)); err != nil {
		return fmt.Errorf("registry: delete %s:%s: %w", repo, tag, err)
	}

	return nil
}

// ImportImage copies an image from an external reference (e.g. "postgres:16",
// "docker.io/library/redis:7", "ghcr.io/foo/bar:latest") into the registry at
// registryHost as repo:tag. If the source resolves to a multi-platform index,
// the linux/amd64 image is selected.
func ImportImage(ctx context.Context, registryHost, source, repo, tag string) error {
	srcRef, err := name.ParseReference(source)
	if err != nil {
		return fmt.Errorf("registry: parse source %s: %w", source, err)
	}

	img, err := remote.Image(srcRef, remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
	if err != nil {
		return fmt.Errorf("registry: pull %s: %w", source, err)
	}

	dstRef, err := name.NewTag(fmt.Sprintf("%s/%s:%s", registryHost, repo, tag), name.Insecure)
	if err != nil {
		return fmt.Errorf("registry: parse destination %s/%s:%s: %w", registryHost, repo, tag, err)
	}

	if err := remote.Write(dstRef, img, remote.WithContext(ctx)); err != nil {
		return fmt.Errorf("registry: write %s/%s:%s: %w", registryHost, repo, tag, err)
	}

	return nil
}

// DefaultRepoTag derives a (repo, tag) pair for ImportImage's destination
// from a source reference, e.g. "postgres:16" -> ("postgres", "16") and
// "bitnami/redis:7.2" -> ("redis", "7.2"). Used to pre-fill the import form
// and as the fallback when the caller doesn't override repo/tag.
func DefaultRepoTag(source string) (repo, tag string) {
	ref, err := name.ParseReference(source)
	if err != nil {
		return "", "latest"
	}
	repo = path.Base(ref.Context().RepositoryStr())
	if t, ok := ref.(name.Tag); ok {
		tag = t.TagStr()
	} else {
		tag = "latest"
	}
	return repo, tag
}
