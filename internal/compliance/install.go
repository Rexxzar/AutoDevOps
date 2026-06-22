package compliance

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Pinned scanner versions — the ones this gate was built and tested against.
// Bump together with any behavioural changes to the parsers.
const (
	gitleaksVersion = "8.18.4"
	hadolintVersion = "2.12.0"
	tfsecVersion    = "1.28.13"
	trivyVersion    = "0.71.2"
)

// toolSpec describes how to fetch one scanner's prebuilt binary from its GitHub
// releases for a given OS/arch. member is the path inside an archive to extract
// ("" means the asset IS the raw binary).
type toolSpec struct {
	name    string
	version string
	// asset returns the release asset filename and (for archives) the binary
	// member to extract, or ok=false if there is no prebuilt binary for this
	// platform. baseURL+asset is the download URL.
	baseURL string
	asset   func(goos, goarch string) (asset, member string, ok bool)
}

// tools is the registry of scanners auto-installed at startup. Order is
// cheapest-first so a slow Trivy download never delays the others landing.
var tools = []toolSpec{
	{
		name: "hadolint", version: hadolintVersion,
		baseURL: "https://github.com/hadolint/hadolint/releases/download/v" + hadolintVersion + "/",
		asset: func(goos, goarch string) (string, string, bool) {
			switch goos {
			case "linux":
				if goarch == "arm64" {
					return "hadolint-Linux-arm64", "", true
				}
				return "hadolint-Linux-x86_64", "", true
			case "darwin":
				return "hadolint-Darwin-x86_64", "", true // universal enough via Rosetta
			case "windows":
				return "hadolint-Windows-x86_64.exe", "", true
			}
			return "", "", false
		},
	},
	{
		name: "tfsec", version: tfsecVersion,
		baseURL: "https://github.com/aquasecurity/tfsec/releases/download/v" + tfsecVersion + "/",
		asset: func(goos, goarch string) (string, string, bool) {
			arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[goarch]
			if arch == "" {
				return "", "", false
			}
			switch goos {
			case "linux", "darwin":
				return fmt.Sprintf("tfsec-%s-%s", goos, arch), "", true
			case "windows":
				return fmt.Sprintf("tfsec-windows-%s.exe", arch), "", true
			}
			return "", "", false
		},
	},
	{
		name: "gitleaks", version: gitleaksVersion,
		baseURL: "https://github.com/gitleaks/gitleaks/releases/download/v" + gitleaksVersion + "/",
		asset: func(goos, goarch string) (string, string, bool) {
			arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
			if arch == "" {
				return "", "", false
			}
			switch goos {
			case "linux", "darwin":
				return fmt.Sprintf("gitleaks_%s_%s_%s.tar.gz", gitleaksVersion, goos, arch), "gitleaks", true
			case "windows":
				return fmt.Sprintf("gitleaks_%s_windows_%s.zip", gitleaksVersion, arch), "gitleaks.exe", true
			}
			return "", "", false
		},
	},
	{
		name: "trivy", version: trivyVersion,
		baseURL: "https://github.com/aquasecurity/trivy/releases/download/v" + trivyVersion + "/",
		asset: func(goos, goarch string) (string, string, bool) {
			arch := map[string]string{"amd64": "64bit", "arm64": "ARM64"}[goarch]
			if arch == "" {
				return "", "", false
			}
			switch goos {
			case "linux":
				return fmt.Sprintf("trivy_%s_Linux-%s.tar.gz", trivyVersion, arch), "trivy", true
			case "darwin":
				return fmt.Sprintf("trivy_%s_macOS-%s.tar.gz", trivyVersion, arch), "trivy", true
			case "windows":
				return fmt.Sprintf("trivy_%s_windows-%s.zip", trivyVersion, arch), "trivy.exe", true
			}
			return "", "", false
		},
	},
}

// EnsureTools makes the four compliance scanners available, mirroring
// terraform.EnsureBinary's "detect on PATH or download a pinned version into a
// user-writable dir" approach for static, daemon-less binaries (contrast
// Docker's privileged-daemon detect-only path). installDir is prepended to PATH
// up front so any already-downloaded binary is picked up by the runners'
// exec.LookPath without per-runner changes. Each tool is best-effort: a failed
// download is logged and that scanner simply stays unavailable (RunAll/ScanSource
// already skip absent scanners). Intended to be called once at startup, in a
// goroutine — the downloads (~90MB total, Trivy dominates) should not block boot.
func EnsureTools(ctx context.Context, installDir string) {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		slog.Warn("compliance: cannot create tools dir, auto-install disabled", "dir", installDir, "error", err)
		return
	}
	prependPath(installDir)

	for _, t := range tools {
		// Already resolvable (on the real PATH, or downloaded on a prior run
		// since installDir is now on PATH)? Nothing to do.
		if _, err := exec.LookPath(t.name); err == nil {
			continue
		}
		if err := installTool(ctx, t, installDir); err != nil {
			slog.Warn("compliance: scanner auto-install failed", "tool", t.name, "error", err)
			continue
		}
		slog.Info("compliance: scanner installed", "tool", t.name, "version", t.version, "dir", installDir)
	}
}

// prependPath puts dir at the front of the process PATH (idempotently) so
// exec.LookPath resolves the managed binaries.
func prependPath(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	cur := os.Getenv("PATH")
	for _, p := range filepath.SplitList(cur) {
		if p == abs {
			return
		}
	}
	if cur == "" {
		_ = os.Setenv("PATH", abs)
		return
	}
	_ = os.Setenv("PATH", abs+string(os.PathListSeparator)+cur)
}

// installTool downloads one scanner and writes its executable to installDir.
func installTool(ctx context.Context, t toolSpec, installDir string) error {
	asset, member, ok := t.asset(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("no prebuilt %s for %s/%s", t.name, runtime.GOOS, runtime.GOARCH)
	}

	target := filepath.Join(installDir, t.name)
	if runtime.GOOS == "windows" {
		target += ".exe"
	}

	url := t.baseURL + asset
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %s", url, resp.Status)
	}

	switch {
	case strings.HasSuffix(asset, ".tar.gz"):
		return extractTarGz(resp.Body, member, target)
	case strings.HasSuffix(asset, ".zip"):
		return extractZip(resp.Body, member, target)
	default:
		return writeExecutable(resp.Body, target)
	}
}

// extractTarGz streams a .tar.gz and writes the entry whose base name matches
// member to target as an executable.
func extractTarGz(r io.Reader, member, target string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("member %q not found in archive", member)
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == member {
			return writeExecutable(tr, target)
		}
	}
}

// extractZip buffers a .zip to a temp file (zip needs random access) and writes
// the entry matching member to target as an executable.
func extractZip(r io.Reader, member, target string) error {
	tmp, err := os.CreateTemp("", "autodevops-tool-*.zip")
	if err != nil {
		return fmt.Errorf("temp zip: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("buffer zip: %w", err)
	}
	tmp.Close()

	zr, err := zip.OpenReader(tmp.Name())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != member {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip member: %w", err)
		}
		defer rc.Close()
		return writeExecutable(rc, target)
	}
	return fmt.Errorf("member %q not found in zip", member)
}

// writeExecutable writes r to a temp file alongside target and atomically
// renames it into place with 0o755, so a partial download never leaves a
// half-written binary that masks future retries.
func writeExecutable(r io.Reader, target string) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tool-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write binary: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
