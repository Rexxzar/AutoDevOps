package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	packclient "github.com/buildpacks/pack/pkg/client"
	"github.com/buildpacks/pack/pkg/image"
	gogit "github.com/go-git/go-git/v5"

	"github.com/Rexxzar/autodevops/internal/sse"
)

// slogPackLogger bridges pack's logging.Logger interface to Go's standard slog
// so build progress flows through the same structured log pipeline as the rest
// of the server rather than writing to a raw io.Writer.
type slogPackLogger struct{}

func (l *slogPackLogger) Debug(msg string)                   { slog.Debug(msg, "source", "pack") }
func (l *slogPackLogger) Debugf(f string, v ...interface{}) { slog.Debug(fmt.Sprintf(f, v...), "source", "pack") }
func (l *slogPackLogger) Info(msg string)                    { slog.Info(msg, "source", "pack") }
func (l *slogPackLogger) Infof(f string, v ...interface{})   { slog.Info(fmt.Sprintf(f, v...), "source", "pack") }
func (l *slogPackLogger) Warn(msg string)                    { slog.Warn(msg, "source", "pack") }
func (l *slogPackLogger) Warnf(f string, v ...interface{})   { slog.Warn(fmt.Sprintf(f, v...), "source", "pack") }
func (l *slogPackLogger) Error(msg string)                   { slog.Error(msg, "source", "pack") }
func (l *slogPackLogger) Errorf(f string, v ...interface{})  { slog.Error(fmt.Sprintf(f, v...), "source", "pack") }
func (l *slogPackLogger) Writer() io.Writer                  { return io.Discard }
func (l *slogPackLogger) IsVerbose() bool                    { return false }

// imageRefs returns both address variants for a deployment's image:
//   - pushRef — what pack pushes to (registryPush/<name>:<sha12>, externally reachable)
//   - pullRef — what k8s manifests reference (registryPull/<name>:<sha12>, in-cluster)
//
// The two differ because the kind local registry is exposed on localhost:5001 from
// the host but is resolved as registry.local:5000 inside the cluster.
func (o *Orchestrator) imageRefs(name, sha12 string) (pushRef, pullRef string) {
	return fmt.Sprintf("%s/%s:%s", o.registryPush, name, sha12),
		fmt.Sprintf("%s/%s:%s", o.registryPull, name, sha12)
}

// headSHA12 reads the cloned repo's HEAD commit and returns the first 12 hex
// chars — matches the short-SHA convention used by GitHub, Flux, and Helm.
func headSHA12(repoPath string) (string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("deploy: open repo for SHA: %w", err)
	}
	ref, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("deploy: resolve HEAD: %w", err)
	}
	return ref.Hash().String()[:12], nil
}

// buildImage calls Paketo Buildpacks (pack Go library) to build + push the OCI
// image from the source at repoPath. On success it persists image_ref and
// transitions status to "deploying". On build failure it transitions to "failed".
// Both outcomes are published as "deploy.status" SSE events.
//
// The pack client is initialised per call — cheap (just an HTTP client to the
// Docker daemon) and safe across daemon restarts between deployments.
func (o *Orchestrator) buildImage(ctx context.Context, deploymentID, deploymentName, repoPath string) error {
	sha12, err := headSHA12(repoPath)
	if err != nil {
		return err
	}

	pushRef, pullRef := o.imageRefs(deploymentName, sha12)

	slog.Info("deploy: starting pack build",
		"deployment_id", deploymentID,
		"push_ref", pushRef,
		"builder", o.builderImage,
	)

	packClient, err := packclient.NewClient(packclient.WithLogger(&slogPackLogger{}))
	if err != nil {
		return fmt.Errorf("deploy: init pack client: %w", err)
	}

	buildErr := packClient.Build(ctx, packclient.BuildOptions{
		Image:      pushRef,
		Builder:    o.builderImage,
		AppPath:    repoPath,
		Publish:    true,
		PullPolicy: image.PullIfNotPresent,
	})
	if buildErr != nil {
		// Record failure in DB + SSE; return error so ProcessPush can log it.
		if dep, pErr := o.persistStatus(ctx, deploymentID, "failed"); pErr != nil {
			slog.Error("deploy: persist failed status", "deployment_id", deploymentID, "error", pErr)
		} else {
			o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
		}
		return fmt.Errorf("deploy: pack build %s: %w", pushRef, buildErr)
	}

	dep, err := o.persistImageRef(ctx, deploymentID, pullRef)
	if err != nil {
		return err
	}
	o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
	slog.Info("deploy: image built and pushed",
		"deployment_id", deploymentID,
		"image_ref", pullRef,
	)
	return nil
}
