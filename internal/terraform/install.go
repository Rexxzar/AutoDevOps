package terraform

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-version"
	install "github.com/hashicorp/hc-install"
	"github.com/hashicorp/hc-install/fs"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/hc-install/src"
)

// pinnedVersion is the terraform CLI version this engine was built and golden-
// tested against (matches design notes Dev Notes and the setup-terraform step in
// CI). Keep these in sync when bumping.
const pinnedVersion = "1.15.5"

// EnsureBinary resolves a path to a terraform binary matching pinnedVersion,
// silently auto-installing one if needed — the locked approach for terraform
// (a static binary, unlike Docker's privileged-daemon detect-only path): look
// for a matching version already on $PATH first, and only download a pinned
// release into installDir (a user-writable directory, no elevated privileges)
// if none is found.
func EnsureBinary(ctx context.Context, installDir string) (string, error) {
	v, err := version.NewVersion(pinnedVersion)
	if err != nil {
		return "", fmt.Errorf("terraform: parse pinned version %q: %w", pinnedVersion, err)
	}
	constraints, err := version.NewConstraint("= " + pinnedVersion)
	if err != nil {
		return "", fmt.Errorf("terraform: parse version constraint: %w", err)
	}

	installer := install.NewInstaller()
	execPath, err := installer.Ensure(ctx, []src.Source{
		&fs.Version{
			Product:     product.Terraform,
			Constraints: constraints,
		},
		&releases.ExactVersion{
			Product:    product.Terraform,
			Version:    v,
			InstallDir: installDir,
		},
	})
	if err != nil {
		return "", fmt.Errorf("terraform: ensure binary v%s: %w", pinnedVersion, err)
	}
	return execPath, nil
}
