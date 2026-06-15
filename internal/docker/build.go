package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/build"
	archive "github.com/moby/go-archive"
	"github.com/moby/patternmatcher/ignorefile"
)

// BuildImage builds the Dockerfile at contextDir's root via the Docker SDK and
// tags the result tag in the host daemon's local image store, implementing
// internal/deploy.ImagePusher's BuildImage method — the Dockerfile-path
// counterpart to buildImage's Paketo (pack) branch. The caller
// (deploy.buildImage) pushes tag via PushImage afterward, exactly as it does
// for pack-built images — both paths land a tagged image in the same local
// daemon store. contextDir's .dockerignore, if present, is honored. logLine,
// if non-nil, is called with each build-output line — the deploy.log SSE
// source for the Docker build path (mirrors slogPackLogger for the pack path).
func (m *Manager) BuildImage(ctx context.Context, contextDir, tag string, logLine func(string)) error {
	excludes, err := readDockerignore(contextDir)
	if err != nil {
		return fmt.Errorf("docker: read .dockerignore: %w", err)
	}

	// archive.TarWithOptions silently returns an empty tar (no error) for a
	// relative srcPath on Windows, which the daemon then reports as a missing
	// Dockerfile — resolve to an absolute path first.
	absContextDir, err := filepath.Abs(contextDir)
	if err != nil {
		return fmt.Errorf("docker: resolve build context %s: %w", contextDir, err)
	}

	tarCtx, err := archive.TarWithOptions(absContextDir, &archive.TarOptions{
		ExcludePatterns: excludes,
	})
	if err != nil {
		return fmt.Errorf("docker: tar build context %s: %w", contextDir, err)
	}
	defer tarCtx.Close()

	resp, err := m.client.ImageBuild(ctx, tarCtx, build.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return fmt.Errorf("docker: image build %s: %w", tag, err)
	}
	defer resp.Body.Close()

	return streamBuildOutput(tag, resp.Body, logLine)
}

// readDockerignore returns the exclusion patterns from contextDir's
// .dockerignore, or nil if the file doesn't exist.
func readDockerignore(contextDir string) ([]string, error) {
	f, err := os.Open(filepath.Join(contextDir, ".dockerignore"))
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("open .dockerignore: %w", err)
	}
	defer f.Close()
	return ignorefile.ReadAll(f)
}

// streamBuildOutput decodes the ImageBuild response's JSON-lines stream and
// routes each line through slog with "source":"docker-build" — mirrors
// PushImage's JSON-stream error handling and slogPackLogger's routing of pack
// build output, giving the Docker build path the same structured-log
// visibility as the Paketo path. logLine, if non-nil, is also called with each
// stream line — the deploy.log SSE source for this build path.
func streamBuildOutput(tag string, r io.Reader, logLine func(string)) error {
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("docker: read build response for %s: %w", tag, err)
		}
		if msg.Error != "" {
			errMsg := msg.Error
			if msg.ErrorDetail.Message != "" {
				errMsg = msg.ErrorDetail.Message
			}
			return fmt.Errorf("docker: build %s: %s", tag, errMsg)
		}
		if line := strings.TrimRight(msg.Stream, "\n"); line != "" {
			slog.Info(line, "source", "docker-build")
			if logLine != nil {
				logLine(line)
			}
		}
	}
}
