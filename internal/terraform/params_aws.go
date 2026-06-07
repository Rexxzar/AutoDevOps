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
	VPCCIDR string

	// EC2 provisions a single VM (subnet, security group, instance) wired
	// to the deployment's VPC. Nil means no EC2 instance was selected.
	EC2 *EC2Params
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
	if _, _, err := net.ParseCIDR(p.VPCCIDR); err != nil {
		return fmt.Errorf("vpc_cidr %q: %w", p.VPCCIDR, err)
	}
	if p.EC2 != nil {
		if err := p.EC2.Validate(); err != nil {
			return fmt.Errorf("ec2: %w", err)
		}
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
