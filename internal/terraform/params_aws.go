package terraform

import (
	"fmt"
	"net"
)

// AWSStackID is the registered template for the combined AWS deployment
// stack. T08 extends params_aws.go and templates/aws/stack.tf.tmpl with
// EC2, RDS, and ECS — this seed covers VPC only, enough to prove the
// render → format → validate pipeline end to end.
const AWSStackID TemplateID = "aws-stack"

// AWSStackParams is the full desired-state parameter set for one AWS
// deployment's combined Terraform stack.
type AWSStackParams struct {
	// Name tags the generated resources, identifying the deployment.
	Name string
	// Region is the AWS region to provision into, e.g. "us-east-1".
	Region string
	// VPCCIDR is the CIDR block for the deployment's VPC, e.g. "10.0.0.0/16".
	VPCCIDR string
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
	return nil
}
