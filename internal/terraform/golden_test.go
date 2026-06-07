package terraform

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update regenerates golden fixtures from the current template output.
// Run: go test ./internal/terraform/... -run TestGolden -update
var update = flag.Bool("update", false, "update golden files")

// goldenCases pairs each registered template with the fixed example params
// used to produce its checked-in fixture. Add an entry here whenever T08/T09
// register a new template so it gets the same byte-for-byte and (optionally)
// terraform fmt/validate coverage.
var goldenCases = []struct {
	name   string
	id     TemplateID
	params Params
}{
	{
		name: "aws-stack",
		id:   AWSStackID,
		params: AWSStackParams{
			Name:    "demo",
			Region:  "us-east-1",
			VPCCIDR: "10.0.0.0/16",
		},
	},
}

func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name+".tf")
}

// TestGolden renders each registered template with fixed example params and
// byte-compares the canonically-formatted output against a checked-in fixture.
// This is the fast path: pure Go, no terraform binary required.
func TestGolden(t *testing.T) {
	e := testEngine(t)

	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Render(tc.id, tc.params)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			path := goldenPath(tc.name)

			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden file: %v", err)
				}
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden file %s (run with -update to create it): %v", path, err)
			}

			if string(got) != string(want) {
				t.Errorf("rendered output for %q does not match %s\n--- got ---\n%s\n--- want ---\n%s", tc.id, path, got, want)
			}
		})
	}
}
