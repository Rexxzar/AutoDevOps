package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestSanitizeSecretKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/opt/secrets/firebase.json", "opt_secrets_firebase.json"},
		{"/etc/app.conf", "etc_app.conf"},
		{"relative/path.txt", "relative_path.txt"},
		{"single.txt", "single.txt"},
	}
	for _, tc := range cases {
		if got := sanitizeSecretKey(tc.in); got != tc.want {
			t.Errorf("sanitizeSecretKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteSecretFilesEmpty(t *testing.T) {
	m := &Manager{workDir: t.TempDir()}
	mounts, err := m.writeSecretFiles("my-stack", "app-plant", nil)
	if err != nil {
		t.Fatalf("writeSecretFiles: %v", err)
	}
	if mounts != nil {
		t.Errorf("mounts = %v, want nil for empty secretFiles", mounts)
	}
}

func TestWriteSecretFiles(t *testing.T) {
	workDir := t.TempDir()
	m := &Manager{workDir: workDir}

	secretFiles := map[string][]byte{
		"/opt/secrets/firebase.json": []byte(`{"type":"service_account"}`),
	}
	mounts, err := m.writeSecretFiles("my-stack", "app-plant", secretFiles)
	if err != nil {
		t.Fatalf("writeSecretFiles: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %v", mounts)
	}

	got := mounts[0]
	wantSource := filepath.Join(workDir, "my-stack", "app-plant", "secrets", "opt_secrets_firebase.json")
	if got.Type != mount.TypeBind || got.Source != wantSource || got.Target != "/opt/secrets/firebase.json" || !got.ReadOnly {
		t.Errorf("mount = %+v, want bind %q -> /opt/secrets/firebase.json (read-only)", got, wantSource)
	}

	content, err := os.ReadFile(wantSource)
	if err != nil {
		t.Fatalf("read written secret file: %v", err)
	}
	if string(content) != `{"type":"service_account"}` {
		t.Errorf("secret file content = %q, want firebase JSON", content)
	}
}
