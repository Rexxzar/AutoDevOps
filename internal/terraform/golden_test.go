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
	{
		name: "aws-stack-ec2",
		id:   AWSStackID,
		params: AWSStackParams{
			Name:    "demo",
			Region:  "us-east-1",
			VPCCIDR: "10.0.0.0/16",
			EC2: &EC2Params{
				InstanceType:     "t3.micro",
				SubnetCIDR:       "10.0.1.0/24",
				AvailabilityZone: "us-east-1a",
			},
		},
	},
	{
		name: "aws-stack-rds",
		id:   AWSStackID,
		params: AWSStackParams{
			Name:    "demo",
			Region:  "us-east-1",
			VPCCIDR: "10.0.0.0/16",
			RDS:     &exampleRDSParams,
		},
	},
	{
		name: "aws-stack-ecs",
		id:   AWSStackID,
		params: AWSStackParams{
			Name:    "demo",
			Region:  "us-east-1",
			VPCCIDR: "10.0.0.0/16",
			ECS:     &exampleECSParams,
		},
	},
	{
		name: "aws-stack-full",
		id:   AWSStackID,
		params: AWSStackParams{
			Name:    "demo",
			Region:  "us-east-1",
			VPCCIDR: "10.0.0.0/16",
			EC2: &EC2Params{
				InstanceType:     "t3.micro",
				SubnetCIDR:       "10.0.1.0/24",
				AvailabilityZone: "us-east-1a",
			},
			RDS: &exampleRDSParams,
			ECS: &exampleECSParams,
		},
	},
}

// exampleRDSParams and exampleECSParams are shared example values, reused
// across the standalone and "full" golden cases — their subnet CIDRs are
// chosen to not overlap each other or the EC2 example's 10.0.1.0/24, since
// aws-stack-full renders all three into the same VPC.
var exampleRDSParams = RDSParams{
	Engine:           "postgres",
	EngineVersion:    "16.4",
	InstanceClass:    "db.t3.micro",
	AllocatedStorage: 20,
	Username:         "appadmin",
	Port:             5432,
	Subnets: []AWSSubnet{
		{CIDR: "10.0.2.0/24", AvailabilityZone: "us-east-1a"},
		{CIDR: "10.0.3.0/24", AvailabilityZone: "us-east-1b"},
	},
}

var exampleECSParams = ECSParams{
	ContainerImage: "public.ecr.aws/nginx/nginx:1.27",
	ContainerPort:  80,
	CPU:            256,
	Memory:         512,
	DesiredCount:   1,
	Subnets: []AWSSubnet{
		{CIDR: "10.0.4.0/24", AvailabilityZone: "us-east-1a"},
		{CIDR: "10.0.5.0/24", AvailabilityZone: "us-east-1b"},
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
