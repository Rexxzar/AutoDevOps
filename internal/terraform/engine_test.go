package terraform

import (
	"strings"
	"testing"
)

// validRDSSubnets and validECSSubnets are minimal valid subnet sets (2
// distinct AZs) reused across the validation-rejection table so each case
// can fail on exactly the field it's testing.
var validRDSSubnets = []AWSSubnet{
	{CIDR: "10.0.2.0/24", AvailabilityZone: "us-east-1a"},
	{CIDR: "10.0.3.0/24", AvailabilityZone: "us-east-1b"},
}

var validECSSubnets = []AWSSubnet{
	{CIDR: "10.0.4.0/24", AvailabilityZone: "us-east-1a"},
	{CIDR: "10.0.5.0/24", AvailabilityZone: "us-east-1b"},
}

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
		{"rds unsupported engine", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			RDS: &RDSParams{
				Engine: "oracle-se2", EngineVersion: "19", InstanceClass: "db.t3.micro",
				AllocatedStorage: 20, Username: "appadmin", Port: 1521,
				Subnets: validRDSSubnets,
			},
		}},
		{"rds non-positive storage", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			RDS: &RDSParams{
				Engine: "postgres", EngineVersion: "16.4", InstanceClass: "db.t3.micro",
				AllocatedStorage: 0, Username: "appadmin", Port: 5432,
				Subnets: validRDSSubnets,
			},
		}},
		{"rds too few subnets", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			RDS: &RDSParams{
				Engine: "postgres", EngineVersion: "16.4", InstanceClass: "db.t3.micro",
				AllocatedStorage: 20, Username: "appadmin", Port: 5432,
				Subnets: []AWSSubnet{{CIDR: "10.0.2.0/24", AvailabilityZone: "us-east-1a"}},
			},
		}},
		{"rds subnets reuse availability zone", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			RDS: &RDSParams{
				Engine: "postgres", EngineVersion: "16.4", InstanceClass: "db.t3.micro",
				AllocatedStorage: 20, Username: "appadmin", Port: 5432,
				Subnets: []AWSSubnet{
					{CIDR: "10.0.2.0/24", AvailabilityZone: "us-east-1a"},
					{CIDR: "10.0.3.0/24", AvailabilityZone: "us-east-1a"},
				},
			},
		}},
		{"ecs missing image", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			ECS: &ECSParams{
				ContainerPort: 80, CPU: 256, Memory: 512, DesiredCount: 1,
				Subnets: validECSSubnets,
			},
		}},
		{"ecs non-positive desired count", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			ECS: &ECSParams{
				ContainerImage: "nginx:latest", ContainerPort: 80, CPU: 256, Memory: 512,
				DesiredCount: 0, Subnets: validECSSubnets,
			},
		}},
		{"ecs too few subnets", AWSStackParams{
			Name: "demo", Region: "us-east-1", VPCCIDR: "10.0.0.0/16",
			ECS: &ECSParams{
				ContainerImage: "nginx:latest", ContainerPort: 80, CPU: 256, Memory: 512,
				DesiredCount: 1,
				Subnets:      []AWSSubnet{{CIDR: "10.0.4.0/24", AvailabilityZone: "us-east-1a"}},
			},
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

func TestRenderWithRDSIncludesDatabaseResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AWSStackID, AWSStackParams{
		Name:    "demo",
		Region:  "us-east-1",
		VPCCIDR: "10.0.0.0/16",
		RDS: &RDSParams{
			Engine:           "postgres",
			EngineVersion:    "16.4",
			InstanceClass:    "db.t3.micro",
			AllocatedStorage: 20,
			Username:         "appadmin",
			Port:             5432,
			Subnets:          validRDSSubnets,
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`resource "aws_subnet" "rds_0"`,
		`resource "aws_subnet" "rds_1"`,
		`resource "aws_db_subnet_group" "rds"`,
		`resource "aws_security_group" "rds"`,
		`resource "aws_db_instance" "main"`,
		`engine                      = "postgres"`,
		`manage_master_user_password = true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
	// The master password must never appear in generated HCL — AWS
	// manages it via Secrets Manager (manage_master_user_password).
	if strings.Contains(got, "password") && !strings.Contains(got, "manage_master_user_password") {
		t.Errorf("rendered output appears to reference a password field outside manage_master_user_password\ngot:\n%s", got)
	}
}

func TestRenderWithoutRDSOmitsDatabaseResources(t *testing.T) {
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
	for _, notWant := range []string{`aws_db_instance`, `aws_db_subnet_group`} {
		if strings.Contains(got, notWant) {
			t.Errorf("rendered output unexpectedly contains %q (RDS not selected)\ngot:\n%s", notWant, got)
		}
	}
}

func TestRenderWithECSIncludesContainerResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AWSStackID, AWSStackParams{
		Name:    "demo",
		Region:  "us-east-1",
		VPCCIDR: "10.0.0.0/16",
		ECS: &ECSParams{
			ContainerImage: "public.ecr.aws/nginx/nginx:1.27",
			ContainerPort:  80,
			CPU:            256,
			Memory:         512,
			DesiredCount:   1,
			Subnets:        validECSSubnets,
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`resource "aws_ecs_cluster" "main"`,
		`resource "aws_ecs_task_definition" "main"`,
		`resource "aws_ecs_service" "main"`,
		`resource "aws_iam_role" "ecs_execution"`,
		`resource "aws_iam_role_policy_attachment" "ecs_execution"`,
		`resource "aws_lb" "main"`,
		`resource "aws_lb_target_group" "main"`,
		`resource "aws_lb_listener" "main"`,
		`image     = "public.ecr.aws/nginx/nginx:1.27"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRenderWithoutECSOmitsContainerResources(t *testing.T) {
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
	for _, notWant := range []string{`aws_ecs_cluster`, `aws_lb`, `aws_iam_role`} {
		if strings.Contains(got, notWant) {
			t.Errorf("rendered output unexpectedly contains %q (ECS not selected)\ngot:\n%s", notWant, got)
		}
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	e := testEngine(t)

	if _, err := e.Render(TemplateID("does-not-exist"), AWSStackParams{}); err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
}
