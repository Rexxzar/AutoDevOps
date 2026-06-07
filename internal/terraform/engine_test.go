package terraform

import (
	"strings"
	"testing"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New([]Definition{
		{ID: AWSStackID, Provider: ProviderAWS, Path: "templates/aws/stack.tf.tmpl"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestRenderProducesFormattedHCL(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AWSStackID, AWSStackParams{
		Name:    "demo",
		Region:  "us-east-1",
		VPCCIDR: "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`region = "us-east-1"`,
		`cidr_block           = "10.0.0.0/16"`,
		`Name = "demo"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRenderRejectsInvalidParams(t *testing.T) {
	e := testEngine(t)

	cases := []struct {
		name   string
		params AWSStackParams
	}{
		{"missing name", AWSStackParams{Region: "us-east-1", VPCCIDR: "10.0.0.0/16"}},
		{"missing region", AWSStackParams{Name: "demo", VPCCIDR: "10.0.0.0/16"}},
		{"malformed cidr", AWSStackParams{Name: "demo", Region: "us-east-1", VPCCIDR: "not-a-cidr"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Render(AWSStackID, tc.params); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	e := testEngine(t)

	if _, err := e.Render(TemplateID("does-not-exist"), AWSStackParams{}); err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
}
