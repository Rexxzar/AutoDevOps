package terraform

import (
	"fmt"
	"net"
)

// AWSStackID is the registered template for the combined AWS deployment
// stack. T08 extends params_aws.go and templates/aws/stack.tf.tmpl with
// EC2, RDS, and ECS — keep extending this same file/struct rather than
// creating parallel ones (one-combined-stack-per-deployment model).
const AWSStackID TemplateID = "aws-stack"

// AWSStackParams is the full desired-state parameter set for one AWS
// deployment's combined Terraform stack. The VPC is always present; every
// other resource is opt-in via a nil-able sub-struct — a nil field means
// the user didn't select that resource for this deployment, and the
// template renders nothing for it (see {{ if .EC2 }} in stack.tf.tmpl).
type AWSStackParams struct {
	// Name tags the generated resources, identifying the deployment.
	Name string
	// Region is the AWS region to provision into, e.g. "us-east-1".
	Region string
	// VPCCIDR is the CIDR block for the deployment's VPC, e.g. "10.0.0.0/16".
	// Ignored when ExistingVPCID is set.
	VPCCIDR string
	// ExistingVPCID, if set, reuses an existing VPC via a `data "aws_vpc"`
	// block instead of creating a new one — the "use existing resource"
	// picker, backed by the VPC IDs
	// returned from GET /cloud/accounts/{id}/networks. VPCCIDR is ignored
	// when this is set.
	ExistingVPCID string

	// EC2 provisions a single VM (subnet, security group, instance) wired
	// to the deployment's VPC. Nil means no EC2 instance was selected.
	EC2 *EC2Params
	// RDS provisions a managed database instance wired to the deployment's
	// VPC. Nil means no database was selected.
	RDS *RDSParams
	// ECS provisions a containerized service on Fargate behind an ALB,
	// wired to the deployment's VPC. Nil means no container service was
	// selected.
	ECS *ECSParams
	// ECR provisions a container image repository alongside this
	// deployment's other AWS resources. Nil means no ECR repository was
	// selected.
	ECR *ECRParams
}

// Validate checks that all fields needed to render a valid AWS stack are
// present and well-formed, returning a friendly error for the first problem
// found so the CLI/dashboard can surface it before rendering.
func (p AWSStackParams) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if p.Region == "" {
		return fmt.Errorf("region is required")
	}
	if p.ExistingVPCID == "" {
		if _, _, err := net.ParseCIDR(p.VPCCIDR); err != nil {
			return fmt.Errorf("vpc_cidr %q: %w", p.VPCCIDR, err)
		}
	}
	if p.EC2 != nil {
		if err := p.EC2.Validate(); err != nil {
			return fmt.Errorf("ec2: %w", err)
		}
	}
	if p.RDS != nil {
		if err := p.RDS.Validate(); err != nil {
			return fmt.Errorf("rds: %w", err)
		}
	}
	if p.ECS != nil {
		if err := p.ECS.Validate(); err != nil {
			return fmt.Errorf("ecs: %w", err)
		}
	}
	if p.ECR != nil {
		if err := p.ECR.Validate(); err != nil {
			return fmt.Errorf("ecr: %w", err)
		}
	}
	return nil
}

// AWSSubnet places one subnet within the deployment's VPC. It's shared by
// any resource that must spread across multiple availability zones — RDS
// (DB subnet group) and ECS (ALB + service placement both require it).
type AWSSubnet struct {
	// CIDR is the subnet's CIDR block, e.g. "10.0.2.0/24" — must fit
	// within the stack's VPCCIDR and not overlap any other subnet.
	CIDR string
	// AvailabilityZone places the subnet, e.g. "us-east-1a".
	AvailabilityZone string
}

// Validate checks the subnet's fields are present and well-formed.
func (s AWSSubnet) Validate() error {
	if _, _, err := net.ParseCIDR(s.CIDR); err != nil {
		return fmt.Errorf("cidr %q: %w", s.CIDR, err)
	}
	if s.AvailabilityZone == "" {
		return fmt.Errorf("availability_zone is required")
	}
	return nil
}

// validateSubnets checks that at least min subnets are given, each is
// individually valid, and they span distinct availability zones — the
// baseline AWS requires for both DB subnet groups and ALB placement.
func validateSubnets(subnets []AWSSubnet, min int) error {
	if len(subnets) < min {
		return fmt.Errorf("at least %d subnets are required, got %d", min, len(subnets))
	}
	seen := make(map[string]bool, len(subnets))
	for i, s := range subnets {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("subnet %d: %w", i, err)
		}
		if seen[s.AvailabilityZone] {
			return fmt.Errorf("subnet %d: availability_zone %q is reused — subnets must span distinct zones", i, s.AvailabilityZone)
		}
		seen[s.AvailabilityZone] = true
	}
	return nil
}

// EC2Params describes a single EC2 instance and the subnet/security group it
// runs in. The AMI is resolved at plan time via a `data "aws_ami"` lookup for
// the latest Amazon Linux image, so the user only chooses sizing and placement.
type EC2Params struct {
	// InstanceType is the EC2 instance size, e.g. "t3.micro".
	InstanceType string
	// SubnetCIDR is the CIDR block for the instance's subnet, e.g.
	// "10.0.1.0/24" — must fit within the stack's VPCCIDR.
	SubnetCIDR string
	// AvailabilityZone places the subnet, e.g. "us-east-1a".
	AvailabilityZone string
}

// Validate checks the EC2 fields are present and well-formed.
func (p EC2Params) Validate() error {
	if p.InstanceType == "" {
		return fmt.Errorf("instance_type is required")
	}
	if _, _, err := net.ParseCIDR(p.SubnetCIDR); err != nil {
		return fmt.Errorf("subnet_cidr %q: %w", p.SubnetCIDR, err)
	}
	if p.AvailabilityZone == "" {
		return fmt.Errorf("availability_zone is required")
	}
	return nil
}

// allowedRDSEngines restricts generated stacks to engines AutoDevOps has
// actually validated end to end — keeps the surface area honest for the
// dissertation evaluation rather than promising untested combinations.
var allowedRDSEngines = map[string]bool{"postgres": true, "mysql": true}

// RDSParams describes a managed database instance: a dedicated subnet group
// spanning multiple AZs, a security group scoped to the VPC's CIDR on the
// configured port, and the instance itself. The master password is generated
// and stored by AWS in Secrets Manager (manage_master_user_password = true)
// — AutoDevOps never generates, transmits, or stores database credentials.
type RDSParams struct {
	// Engine is the database engine — "postgres" or "mysql".
	Engine string
	// EngineVersion is the engine version, e.g. "16" (major-version-only —
	// RDS resolves to its current default minor; the AWS provider suppresses
	// the resulting state diff, so this stays stable as minors are retired).
	EngineVersion string
	// InstanceClass is the RDS instance size, e.g. "db.t3.micro".
	InstanceClass string
	// AllocatedStorage is the storage size in GB.
	AllocatedStorage int
	// Username is the master username (the password is AWS-managed).
	Username string
	// Port is the database port, e.g. 5432 for postgres, 3306 for mysql.
	Port int
	// Subnets places the DB subnet group — at least 2, in distinct AZs,
	// per AWS's requirement for DB subnet groups.
	Subnets []AWSSubnet
}

// Validate checks the RDS fields are present, well-formed, and that the
// engine is one AutoDevOps supports.
func (p RDSParams) Validate() error {
	if !allowedRDSEngines[p.Engine] {
		return fmt.Errorf("engine %q: must be one of postgres, mysql", p.Engine)
	}
	if p.EngineVersion == "" {
		return fmt.Errorf("engine_version is required")
	}
	if p.InstanceClass == "" {
		return fmt.Errorf("instance_class is required")
	}
	if p.AllocatedStorage <= 0 {
		return fmt.Errorf("allocated_storage must be greater than zero")
	}
	if p.Username == "" {
		return fmt.Errorf("username is required")
	}
	if p.Port <= 0 || p.Port > 65535 {
		return fmt.Errorf("port %d: must be between 1 and 65535", p.Port)
	}
	if err := validateSubnets(p.Subnets, 2); err != nil {
		return fmt.Errorf("subnets: %w", err)
	}
	return nil
}

// ECSParams describes a containerized service running on AWS Fargate behind
// an Application Load Balancer — the "full ECS" scope decided for T08:
// cluster, task definition, execution IAM role, service, and ALB. This is
// the heaviest of the three opt-in resource types (~15 wired resources,
// vs EC2's ~3 and RDS's ~4) per the 2026-06-07 scoping note.
type ECSParams struct {
	// ContainerImage is the image to run, e.g. "nginx:latest" or an ECR URI.
	ContainerImage string
	// ContainerPort is the port the container listens on, e.g. 80.
	ContainerPort int
	// CPU is the Fargate task's CPU units, e.g. 256.
	CPU int
	// Memory is the Fargate task's memory in MB, e.g. 512.
	Memory int
	// DesiredCount is the number of running tasks.
	DesiredCount int
	// Subnets place the ALB and service tasks — at least 2, in distinct
	// AZs, per AWS's requirement for internet-facing ALBs.
	Subnets []AWSSubnet
}

// Validate checks the ECS fields are present and well-formed.
func (p ECSParams) Validate() error {
	if p.ContainerImage == "" {
		return fmt.Errorf("container_image is required")
	}
	if p.ContainerPort <= 0 || p.ContainerPort > 65535 {
		return fmt.Errorf("container_port %d: must be between 1 and 65535", p.ContainerPort)
	}
	if p.CPU <= 0 {
		return fmt.Errorf("cpu must be greater than zero")
	}
	if p.Memory <= 0 {
		return fmt.Errorf("memory must be greater than zero")
	}
	if p.DesiredCount <= 0 {
		return fmt.Errorf("desired_count must be greater than zero")
	}
	if err := validateSubnets(p.Subnets, 2); err != nil {
		return fmt.Errorf("subnets: %w", err)
	}
	return nil
}

// ECRParams is an opt-in marker for provisioning an ECR repository alongside
// this deployment's other AWS resources — bundled into the same combined
// stack/state rather than a separate provisioned_resources row, per the
// Phase 8 ECR/ACR design. It carries no fields; a non-nil value is the only
// signal the template checks.
type ECRParams struct{}

// Validate is a no-op — ECRParams carries no fields to check.
func (p ECRParams) Validate() error {
	return nil
}
