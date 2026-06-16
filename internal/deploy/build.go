package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	packclient "github.com/buildpacks/pack/pkg/client"
	"github.com/buildpacks/pack/pkg/image"
	gogit "github.com/go-git/go-git/v5"

	"github.com/Rexxzar/autodevops/internal/sse"
)

// slogPackLogger bridges pack's logging.Logger interface to Go's standard slog
// so build progress flows through the same structured log pipeline as the rest
// of the server rather than writing to a raw io.Writer. logLine, if non-nil, is
// also called with each Info/Warn/Error line — buildImage's deploy.log SSE
// source for the pack build path (mirrors streamBuildOutput's logLine for the
// Docker build path).
type slogPackLogger struct {
	logLine func(string)
}

func (l *slogPackLogger) Debug(msg string)                  { slog.Debug(msg, "source", "pack") }
func (l *slogPackLogger) Debugf(f string, v ...interface{}) { slog.Debug(fmt.Sprintf(f, v...), "source", "pack") }
func (l *slogPackLogger) Info(msg string)                   { slog.Info(msg, "source", "pack"); l.emit(msg) }
func (l *slogPackLogger) Infof(f string, v ...interface{}) {
	msg := fmt.Sprintf(f, v...)
	slog.Info(msg, "source", "pack")
	l.emit(msg)
}
func (l *slogPackLogger) Warn(msg string) { slog.Warn(msg, "source", "pack"); l.emit(msg) }
func (l *slogPackLogger) Warnf(f string, v ...interface{}) {
	msg := fmt.Sprintf(f, v...)
	slog.Warn(msg, "source", "pack")
	l.emit(msg)
}
func (l *slogPackLogger) Error(msg string) { slog.Error(msg, "source", "pack"); l.emit(msg) }
func (l *slogPackLogger) Errorf(f string, v ...interface{}) {
	msg := fmt.Sprintf(f, v...)
	slog.Error(msg, "source", "pack")
	l.emit(msg)
}
func (l *slogPackLogger) Writer() io.Writer { return io.Discard }
func (l *slogPackLogger) IsVerbose() bool   { return false }

func (l *slogPackLogger) emit(line string) {
	if l.logLine != nil {
		l.logLine(line)
	}
}

// imageNameOnly strips the host:port prefix from a registry image ref,
// returning just "name:tag" — used when substituting the lifecycle registry host.
// e.g. "localhost:5001/skyline:abc123" → "skyline:abc123"
func imageNameOnly(ref string) string {
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

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

// hasDockerfile reports whether repoPath (the build-context root) contains a
// Dockerfile, in which case buildImage builds it via the Docker SDK instead
// of Paketo Buildpacks — "Dockerfile-if-present, else Paketo". Mirrors
// internal/compliance/hadolint.go's equivalent check, duplicated per the
// package-boundary convention rather than sharing a one-line helper.
func hasDockerfile(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, "Dockerfile"))
	return err == nil
}

// ensurePackProcfile writes a Procfile to contextDir if the repo is a static
// frontend build (has a "build" npm script but no "start" script and no
// existing Procfile). Without a Procfile, Paketo's lifecycle exits with
// "no default process" and the container crash-loops immediately.
//
// The injected process serves the build output directory (dist/) with
// `npx serve` in SPA mode so React Router works correctly. The Procfile is
// written into the build context (the cloned temp dir), never into the user's
// actual repo.
// spaServerJS is a self-contained Node.js static file server injected into
// static-frontend repos that have a build script but no start script. It
// serves dist/ with SPA fallback (unknown paths → index.html) using only
// Node's built-in http/fs/path modules — no npm packages required. This
// avoids any npm install or PATH issues inside Paketo's runtime layer.
const spaServerJS = `const http = require('http');
const fs = require('fs');
const path = require('path');
const port = parseInt(process.env.PORT || '8080', 10);
const dist = path.join(__dirname, 'dist');
const mime = {
  '.html':'text/html','.js':'application/javascript','.mjs':'application/javascript',
  '.css':'text/css','.json':'application/json','.png':'image/png',
  '.jpg':'image/jpeg','.jpeg':'image/jpeg','.gif':'image/gif',
  '.svg':'image/svg+xml','.ico':'image/x-icon',
  '.woff':'font/woff','.woff2':'font/woff2','.ttf':'font/ttf',
};
http.createServer((req, res) => {
  const url = req.url.split('?')[0].split('#')[0];
  let file = path.join(dist, url);
  try {
    if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(dist, 'index.html');
  } catch (_) { file = path.join(dist, 'index.html'); }
  const ext = path.extname(file).toLowerCase();
  res.writeHead(200, { 'Content-Type': mime[ext] || 'application/octet-stream' });
  fs.createReadStream(file).pipe(res);
}).listen(port, '0.0.0.0', () => console.log('listening on', port));
`

func ensurePackProcfile(contextDir string) {
	// Skip if a Procfile already exists — respect the repo's own definition.
	if _, err := os.Stat(filepath.Join(contextDir, "Procfile")); err == nil {
		return
	}
	pkgPath := filepath.Join(contextDir, "package.json")
	pkg, err := os.ReadFile(pkgPath)
	if err != nil {
		return
	}
	// Use map[string]interface{} (not json.RawMessage) so the roundtrip stays
	// safe: all values become standard Go types, MarshalIndent never embeds
	// raw bytes that could produce malformed JSON.
	var manifest map[string]interface{}
	if err := json.Unmarshal(pkg, &manifest); err != nil {
		return
	}
	scripts, _ := manifest["scripts"].(map[string]interface{})
	_, hasBuild := scripts["build"]
	_, hasStart := scripts["start"]
	if !hasBuild || hasStart {
		return // has a start script or nothing to build — let Paketo decide
	}

	// Static frontend: write a tiny self-contained Node.js SPA server (built-in
	// modules only, no npm packages needed) and add a "start" script so Paketo's
	// npm-start buildpack detects this app as needing node at launch time.
	// Without a start script, the Node Engine buildpack omits node from the
	// launch environment and `node` is not on PATH when the container starts —
	// the Procfile alone is insufficient because Paketo decides launch-layer
	// inclusion during the build phase.
	// Use .cjs extension so Node treats this as CommonJS even when package.json
	// has "type": "module" (which would make .js files ESM, breaking require()).
	_ = os.WriteFile(filepath.Join(contextDir, "autodevops_server.cjs"), []byte(spaServerJS), 0o644)
	if scripts == nil {
		scripts = map[string]interface{}{}
	}
	scripts["start"] = "node autodevops_server.cjs"
	manifest["scripts"] = scripts
	if updated, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		_ = os.WriteFile(pkgPath, updated, 0o644)
	}
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

// buildImage builds + pushes the OCI image from the source at repoPath,
// dispatching on hasDockerfile: a root Dockerfile is built via the Docker SDK
// (internal/docker's BuildImage), otherwise Paketo Buildpacks (pack Go
// library) builds it — "Dockerfile-if-present, else Paketo". contextDir is
// the build root passed to hasDockerfile/BuildImage/pack's AppPath — equal to
// repoPath unless the app has a build_context (a docker-compose import
// scoping the build to a subdirectory), while repoPath itself stays the repo
// root for headSHA12. On success it persists image_ref and transitions status
// to "deploying". On any failure (Docker unavailable, no buildpack detected,
// build error, push error) it transitions to "failed". Both outcomes are
// published as "deploy.status" SSE events.
//
// The pack client is initialised per call — cheap (just an HTTP client to the
// Docker daemon) and safe across daemon restarts between deployments.
func (o *Orchestrator) buildImage(ctx context.Context, appID, appName, repoPath, contextDir string, extraEnv map[string]string) (retErr error) {
	// On any error path, persist "failed" so status never stays stuck at "building".
	defer func() {
		if retErr != nil {
			if dep, err := o.persistStatus(ctx, appID, "failed"); err != nil {
				slog.Error("deploy: persist failed status", "app_id", appID, "error", err)
			} else {
				o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
			}
		}
	}()

	sha12, err := headSHA12(repoPath)
	if err != nil {
		o.publishStep(appID, StepBuild, StepFailed)
		return err
	}

	pushRef, pullRef := o.imageRefs(appName, sha12)

	if o.pusher == nil {
		o.publishStep(appID, StepBuild, StepFailed)
		return fmt.Errorf("deploy: no image pusher configured (Docker daemon unavailable)")
	}

	logLine := func(line string) { o.publishLog(appID, line) }

	o.publishStep(appID, StepBuild, StepActive)
	if hasDockerfile(contextDir) {
		slog.Info("deploy: starting docker build",
			"app_id", appID,
			"push_ref", pushRef,
		)
		if err := o.pusher.BuildImage(ctx, contextDir, pushRef, logLine); err != nil {
			o.publishStep(appID, StepBuild, StepFailed)
			return fmt.Errorf("deploy: docker build %s: %w", pushRef, err)
		}
	} else {
		slog.Info("deploy: starting pack build",
			"app_id", appID,
			"push_ref", pushRef,
			"builder", o.builderImage,
		)

		ensurePackProcfile(contextDir)
		packClient, err := packclient.NewClient(packclient.WithLogger(&slogPackLogger{logLine: logLine}))
		if err != nil {
			o.publishStep(appID, StepBuild, StepFailed)
			return fmt.Errorf("deploy: init pack client: %w", err)
		}

		// Two pack publish modes depending on whether the Docker daemon uses
		// containerd image store:
		//
		// Publish:false (default) — builds into the host daemon's local image
		// store; o.pusher.PushImage pushes from the host afterward. Fails on
		// Linux Docker with containerd storage (exit 62) because the lifecycle
		// containers can't export to containerd's store format.
		//
		// Publish:true (when registryLifecycle is set) — lifecycle containers push
		// directly to the registry address visible from inside Docker (bridge
		// gateway IP, e.g. "172.17.0.1:5001"). Requires the registry to be bound
		// to 0.0.0.0, not 127.0.0.1. The separate PushImage step is skipped since
		// the image is already in the registry.
		//
		// TrustBuilder: pack's default IsKnownTrustedBuilder does an exact string
		// match against builder image names with NO tag, so our configured
		// "paketobuildpacks/builder-jammy-full:latest" (with tag) doesn't match and
		// pack falls back to the untrusted 5-phase flow — which hits a
		// "/layers/group.toml: permission denied" error on this Docker setup.
		// builderImage is server config (not user input) and is one of pack's
		// officially trusted Paketo builders, so trusting it here is safe.
		packPublish := o.registryLifecycle != ""
		packImageRef := pushRef
		if packPublish {
			// Replace the host:port prefix so lifecycle containers can reach the registry.
			packImageRef = o.registryLifecycle + "/" + imageNameOnly(pushRef)
		}
		if err := packClient.Build(ctx, packclient.BuildOptions{
			Image:        packImageRef,
			Builder:      o.builderImage,
			AppPath:      contextDir,
			Publish:      packPublish,
			PullPolicy:   image.PullIfNotPresent,
			TrustBuilder: func(string) bool { return true },
			Env:          extraEnv,
		}); err != nil {
			o.publishStep(appID, StepBuild, StepFailed)
			return fmt.Errorf("deploy: pack build %s: %w", pushRef, err)
		}
	}
	o.publishStep(appID, StepBuild, StepDone)

	o.publishStep(appID, StepPush, StepActive)
	if o.registryLifecycle != "" {
		// Image was published directly to the registry by the lifecycle container
		// under lifecycleRef (gateway-IP addr). Re-tag it under pushRef (localhost
		// addr) so pullRef lookups work — both hostnames reach the same registry.
		lifecycleRef := o.registryLifecycle + "/" + imageNameOnly(pushRef)
		if err := o.pusher.TagImage(ctx, lifecycleRef, pushRef); err != nil {
			// Non-fatal: log and continue.
			slog.Warn("deploy: retag lifecycle image", "from", lifecycleRef, "to", pushRef, "error", err)
		}
	} else {
		if err := o.pusher.PushImage(ctx, pushRef, nil); err != nil {
			o.publishStep(appID, StepPush, StepFailed)
			return fmt.Errorf("deploy: push %s: %w", pushRef, err)
		}
	}
	o.publishStep(appID, StepPush, StepDone)

	dep, err := o.persistImageRef(ctx, appID, pullRef)
	if err != nil {
		return err
	}
	// Success: clear deferred "failed" path by publishing the deploying event here.
	o.hub.Publish(sse.Event{Type: "deploy.status", Payload: dep})
	slog.Info("deploy: image built and pushed",
		"app_id", appID,
		"image_ref", pullRef,
	)
	return nil
}
