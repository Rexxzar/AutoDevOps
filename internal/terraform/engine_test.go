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
		{"ec2 missing instance type", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			EC2: &EC2Params{SubnetCIDR: "10.0.1.0/24", AvailabilityZone: "us-east-1a"},
		}},
		{"ec2 malformed subnet cidr", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			EC2: &EC2Params{InstanceType: "t3.micro", SubnetCIDR: "not-a-cidr", AvailabilityZone: "us-east-1a"},
		}},
		{"ec2 missing availability zone", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			EC2: &EC2Params{InstanceType: "t3.micro", SubnetCIDR: "10.0.1.0/24"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Render(AWSStackID, tc.params); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestRenderWithEC2IncludesInstanceResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AWSStackID, AWSStackParams{
		Name:    "demo",
		Region:  "us-east-1",
		VPCCIDR: "10.0.0.0/16",
		EC2: &EC2Params{
			InstanceType:     "t3.micro",
			SubnetCIDR:       "10.0.1.0/24",
			AvailabilityZone: "us-east-1a",
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`data "aws_ami" "amazon_linux"`,
		`resource "aws_subnet" "ec2"`,
		`resource "aws_security_group" "ec2"`,
		`resource "aws_instance" "main"`,
		`instance_type          = "t3.micro"`,
		`cidr_block        = "10.0.1.0/24"`,
		`availability_zone = "us-east-1a"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRenderWithoutEC2OmitsInstanceResources(t *testing.T) {
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
	for _, notWant := range []string{`aws_instance`, `aws_ami`, `aws_subnet`, `aws_security_group`} {
		if strings.Contains(got, notWant) {
			t.Errorf("rendered output unexpectedly contains %q (EC2 not selected)\ngot:\n%s", notWant, got)
		}
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	e := testEngine(t)

	if _, err := e.Render(TemplateID("does-not-exist"), AWSStackParams{}); err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
}
