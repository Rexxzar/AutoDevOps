package terraform

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// aciFixture returns the rendered azure-stack-aci golden fixture — a
// realistic main.tf containing azurerm_container_group.main, used as input
// to setACIContainerImage's HCL-surgery round-trip.
func aciFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(goldenPath("azure-stack-aci"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestSetACIContainerImage(t *testing.T) {
	cfg := aciFixture(t)

	out, err := setACIContainerImage(cfg, "demoacr1a2b3c4d.azurecr.io/myapp:abc123def456", map[string]string{
		"PORT":     "8080",
		"DATABASE": "postgres",
	})
	if err != nil {
		t.Fatalf("setACIContainerImage: %v", err)
	}

	got := string(out)
	if !regexp.MustCompile(`image\s*=\s*"demoacr1a2b3c4d\.azurecr\.io/myapp:abc123def456"`).MatchString(got) {
		t.Errorf("output missing updated image:\n%s", got)
	}
	if strings.Contains(got, "mcr.microsoft.com/azuredocs/aci-helloworld") {
		t.Errorf("output still references old image:\n%s", got)
	}
	if !strings.Contains(got, "environment_variables") {
		t.Errorf("output missing environment_variables block:\n%s", got)
	}
	if !regexp.MustCompile(`PORT\s*=\s*"8080"`).MatchString(got) {
		t.Errorf("output missing PORT env var:\n%s", got)
	}

	// Re-parsing the output should succeed and still find the container group
	// — confirms the surgery produces valid, re-editable HCL.
	if _, err := setACIContainerImage(out, "demoacr1a2b3c4d.azurecr.io/myapp:def789", nil); err != nil {
		t.Fatalf("re-applying surgery to surgery output: %v", err)
	}
}

func TestSetACIContainerImageNoEnv(t *testing.T) {
	cfg := aciFixture(t)

	out, err := setACIContainerImage(cfg, "demoacr1a2b3c4d.azurecr.io/myapp:abc123def456", nil)
	if err != nil {
		t.Fatalf("setACIContainerImage: %v", err)
	}

	got := string(out)
	if !regexp.MustCompile(`image\s*=\s*"demoacr1a2b3c4d\.azurecr\.io/myapp:abc123def456"`).MatchString(got) {
		t.Errorf("output missing updated image:\n%s", got)
	}
	if strings.Contains(got, "environment_variables") {
		t.Errorf("output should not contain environment_variables when env is nil:\n%s", got)
	}
}

func TestSetACIContainerImageMissingResource(t *testing.T) {
	cfg := []byte(`
resource "azurerm_resource_group" "main" {
  name     = "demo-rg"
  location = "uksouth"
}
`)

	if _, err := setACIContainerImage(cfg, "example.azurecr.io/app:latest", nil); err == nil {
		t.Fatal("expected error for config with no azurerm_container_group.main, got nil")
	}
}
