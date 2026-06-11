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

// validSSHPublicKey is a minimal well-formed key for Azure VM test cases —
// the exact bytes don't matter, only that it starts with "ssh-".
const validSSHPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTestingOnly demo@example.com"

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New([]Definition{
		{ID: AWSStackID, Provider: ProviderAWS, Path: "templates/aws/stack.tf.tmpl"},
		{ID: AzureStackID, Provider: ProviderAzure, Path: "templates/azure/stack.tf.tmpl"},
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
		`cidr_block              = "10.0.1.0/24"`,
		`availability_zone       = "us-east-1a"`,
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

func TestRenderAzureProducesFormattedHCL(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`location = "uksouth"`,
		`address_space       = ["10.0.0.0/16"]`,
		`Name = "demo"`,
		`resource "azurerm_resource_group" "main"`,
		`resource "azurerm_virtual_network" "main"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRenderAzureRejectsInvalidParams(t *testing.T) {
	e := testEngine(t)

	cases := []struct {
		name   string
		params AzureStackParams
	}{
		{"missing name", AzureStackParams{Location: "uksouth", VNetCIDR: "10.0.0.0/16"}},
		{"missing location", AzureStackParams{Name: "demo", VNetCIDR: "10.0.0.0/16"}},
		{"malformed cidr", AzureStackParams{Name: "demo", Location: "uksouth", VNetCIDR: "not-a-cidr"}},
		{"vm missing size", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			VM: &AzureVMParams{SubnetCIDR: "10.0.1.0/24", AdminUsername: "azureuser", SSHPublicKey: validSSHPublicKey},
		}},
		{"vm malformed subnet cidr", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			VM: &AzureVMParams{Size: "Standard_B1s", SubnetCIDR: "not-a-cidr", AdminUsername: "azureuser", SSHPublicKey: validSSHPublicKey},
		}},
		{"vm missing admin username", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			VM: &AzureVMParams{Size: "Standard_B1s", SubnetCIDR: "10.0.1.0/24", SSHPublicKey: validSSHPublicKey},
		}},
		{"vm missing ssh public key", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			VM: &AzureVMParams{Size: "Standard_B1s", SubnetCIDR: "10.0.1.0/24", AdminUsername: "azureuser"},
		}},
		{"vm malformed ssh public key", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			VM: &AzureVMParams{Size: "Standard_B1s", SubnetCIDR: "10.0.1.0/24", AdminUsername: "azureuser", SSHPublicKey: "not-a-key"},
		}},
		{"db missing version", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			DB: &AzureDBParams{
				SKU: "B_Standard_B1ms", StorageMB: 32768,
				AADAdminObjectID: "00000000-0000-0000-0000-000000000000", AADAdminPrincipalName: "alice@example.com", AADAdminPrincipalType: "User",
			},
		}},
		{"db non-positive storage", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			DB: &AzureDBParams{
				Version: "16", SKU: "B_Standard_B1ms", StorageMB: 0,
				AADAdminObjectID: "00000000-0000-0000-0000-000000000000", AADAdminPrincipalName: "alice@example.com", AADAdminPrincipalType: "User",
			},
		}},
		{"db missing aad admin object id", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			DB: &AzureDBParams{
				Version: "16", SKU: "B_Standard_B1ms", StorageMB: 32768,
				AADAdminPrincipalName: "alice@example.com", AADAdminPrincipalType: "User",
			},
		}},
		{"db invalid aad principal type", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			DB: &AzureDBParams{
				Version: "16", SKU: "B_Standard_B1ms", StorageMB: 32768,
				AADAdminObjectID: "00000000-0000-0000-0000-000000000000", AADAdminPrincipalName: "alice@example.com", AADAdminPrincipalType: "Robot",
			},
		}},
		{"aci missing image", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			ACI: &AzureACIParams{ContainerPort: 80, CPU: 1, MemoryGB: 1.5},
		}},
		{"aci non-positive cpu", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			ACI: &AzureACIParams{ContainerImage: "nginx:latest", ContainerPort: 80, CPU: 0, MemoryGB: 1.5},
		}},
		{"aci out of range port", AzureStackParams{
			Name: "demo", Location: "uksouth", VNetCIDR: "10.0.0.0/16",
			ACI: &AzureACIParams{ContainerImage: "nginx:latest", ContainerPort: 70000, CPU: 1, MemoryGB: 1.5},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Render(AzureStackID, tc.params); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestRenderWithVMIncludesInstanceResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
		VM: &AzureVMParams{
			Size:          "Standard_B1s",
			SubnetCIDR:    "10.0.1.0/24",
			AdminUsername: "azureuser",
			SSHPublicKey:  validSSHPublicKey,
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`resource "azurerm_subnet" "vm"`,
		`resource "azurerm_network_security_group" "vm"`,
		`resource "azurerm_public_ip" "vm"`,
		`resource "azurerm_network_interface" "vm"`,
		`resource "azurerm_linux_virtual_machine" "main"`,
		`size                  = "Standard_B1s"`,
		`disable_password_authentication = true`,
		`public_key = "` + validSSHPublicKey + `"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
	// The admin account must be authenticated by SSH key only — never a
	// generated/stored password (mirrors RDS's manage_master_user_password
	// no-stored-credentials principle).
	if strings.Contains(got, "admin_password") {
		t.Errorf("rendered output unexpectedly contains admin_password — VM auth must be SSH-key-only\ngot:\n%s", got)
	}
}

func TestRenderWithoutVMOmitsInstanceResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, notWant := range []string{`azurerm_linux_virtual_machine`, `azurerm_network_interface`, `azurerm_public_ip`, `azurerm_subnet`, `azurerm_network_security_group`} {
		if strings.Contains(got, notWant) {
			t.Errorf("rendered output unexpectedly contains %q (VM not selected)\ngot:\n%s", notWant, got)
		}
	}
}

func TestRenderWithDBIncludesDatabaseResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
		DB: &AzureDBParams{
			Version:               "16",
			SKU:                   "B_Standard_B1ms",
			StorageMB:             32768,
			AADAdminObjectID:      "00000000-0000-0000-0000-000000000000",
			AADAdminPrincipalName: "alice@example.com",
			AADAdminPrincipalType: "User",
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`data "azurerm_client_config" "current"`,
		`resource "azurerm_postgresql_flexible_server" "main"`,
		`resource "azurerm_postgresql_flexible_server_active_directory_administrator" "main"`,
		`resource "azurerm_postgresql_flexible_server_firewall_rule" "azure_services"`,
		`active_directory_auth_enabled = true`,
		`password_auth_enabled         = false`,
		`object_id           = "00000000-0000-0000-0000-000000000000"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
	// No password field of any kind must appear — authentication is Azure AD
	// only (mirrors RDS's manage_master_user_password no-stored-credentials
	// principle: AutoDevOps never generates, transmits, or stores a DB password).
	if strings.Contains(got, "administrator_password") || strings.Contains(strings.ToLower(got), "\"password\"") {
		t.Errorf("rendered output appears to reference a stored database password — auth must be Azure-AD-only\ngot:\n%s", got)
	}
}

func TestRenderWithoutDBOmitsDatabaseResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, notWant := range []string{`azurerm_postgresql_flexible_server`, `azurerm_client_config`} {
		if strings.Contains(got, notWant) {
			t.Errorf("rendered output unexpectedly contains %q (DB not selected)\ngot:\n%s", notWant, got)
		}
	}
}

func TestRenderWithACIIncludesContainerResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
		ACI: &AzureACIParams{
			ContainerImage: "mcr.microsoft.com/azuredocs/aci-helloworld:latest",
			ContainerPort:  80,
			CPU:            1,
			MemoryGB:       1.5,
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	for _, want := range []string{
		`resource "azurerm_container_group" "main"`,
		`image  = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"`,
		`ip_address_type     = "Public"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRenderWithoutACIOmitsContainerResources(t *testing.T) {
	e := testEngine(t)

	out, err := e.Render(AzureStackID, AzureStackParams{
		Name:     "demo",
		Location: "uksouth",
		VNetCIDR: "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := string(out)
	if strings.Contains(got, "azurerm_container_group") {
		t.Errorf("rendered output unexpectedly contains azurerm_container_group (ACI not selected)\ngot:\n%s", got)
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	e := testEngine(t)

	if _, err := e.Render(TemplateID("does-not-exist"), AWSStackParams{}); err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
}
