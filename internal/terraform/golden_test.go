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
	{
		name: "azure-stack",
		id:   AzureStackID,
		params: AzureStackParams{
			Name:     "demo",
			Location: "uksouth",
			VNetCIDR: "10.0.0.0/16",
		},
	},
	{
		name: "azure-stack-vm",
		id:   AzureStackID,
		params: AzureStackParams{
			Name:     "demo",
			Location: "uksouth",
			VNetCIDR: "10.0.0.0/16",
			VM:       &exampleAzureVMParams,
		},
	},
	{
		name: "azure-stack-db",
		id:   AzureStackID,
		params: AzureStackParams{
			Name:     "demo",
			Location: "uksouth",
			VNetCIDR: "10.0.0.0/16",
			DB:       &exampleAzureDBParams,
		},
	},
	{
		name: "azure-stack-aci",
		id:   AzureStackID,
		params: AzureStackParams{
			Name:     "demo",
			Location: "uksouth",
			VNetCIDR: "10.0.0.0/16",
			ACI:      &exampleAzureACIParams,
		},
	},
	{
		name: "azure-stack-full",
		id:   AzureStackID,
		params: AzureStackParams{
			Name:     "demo",
			Location: "uksouth",
			VNetCIDR: "10.0.0.0/16",
			VM:       &exampleAzureVMParams,
			DB:       &exampleAzureDBParams,
			ACI:      &exampleAzureACIParams,
		},
	},
}

// exampleAzureVMParams is the shared example value for the Azure VM golden
// cases. SSHPublicKey must be a *syntactically valid* SSH public key —
// `terraform validate` actually parses admin_ssh_key.public_key as a key
// object, so a placeholder string fails gated validation. This is a freshly
// generated, single-use throwaway keypair (private half discarded); public
// keys aren't secrets, so checking it in is fine.
var exampleAzureVMParams = AzureVMParams{
	Size:          "Standard_B1s",
	SubnetCIDR:    "10.0.1.0/24",
	AdminUsername: "azureuser",
	SSHPublicKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILWSPQrVP9waYpuXgeULzDf6qni7GWmZISD6J2Sy8Qkb demo@example.com",
}

// exampleAzureDBParams and exampleAzureACIParams are shared example values
// for the Azure DB/ACI golden cases. The AAD admin object ID is a
// syntactically-valid-looking but fake GUID — terraform validate doesn't
// resolve it against a real tenant (that only happens at plan/apply), so a
// placeholder is fine here, unlike the VM's SSH key which IS parsed at
// validate time.
var exampleAzureDBParams = AzureDBParams{
	Version:               "16",
	SKU:                   "B_Standard_B1ms",
	StorageMB:             32768,
	AADAdminObjectID:      "00000000-0000-0000-0000-000000000000",
	AADAdminPrincipalName: "alice@example.com",
	AADAdminPrincipalType: "User",
}

var exampleAzureACIParams = AzureACIParams{
	ContainerImage: "mcr.microsoft.com/azuredocs/aci-helloworld:latest",
	ContainerPort:  80,
	CPU:            1,
	MemoryGB:       1.5,
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
