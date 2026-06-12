package console

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
)

// ec2InstanceConnectUser is the SSH login user for EC2 Instance Connect.
// stack.tf.tmpl pins the AMI to Amazon Linux 2023 (al2023-ami-*-x86_64),
// whose default user is "ec2-user" and which ships the EC2 Instance Connect
// agent preinstalled.
const ec2InstanceConnectUser = "ec2-user"

// ec2Connector opens shells / runs commands on AWS EC2 instances via EC2
// Instance Connect: push an ephemeral SSH public key (60s TTL, scoped to one
// instance + OS user) then dial SSH directly to the instance's public IP.
type ec2Connector struct {
	registry *Registry
}

// target extracts the instance ID and public IP from the resource's
// terraform outputs (ec2_instance_id, ec2_public_ip — stack.tf.tmpl).
func (c *ec2Connector) target(res Resource) (instanceID, publicIP string, err error) {
	instanceID, _ = res.Outputs["ec2_instance_id"].(string)
	publicIP, _ = res.Outputs["ec2_public_ip"].(string)
	if instanceID == "" || publicIP == "" {
		return "", "", fmt.Errorf("console: resource has no ec2_instance_id/ec2_public_ip outputs — apply it first")
	}
	return instanceID, publicIP, nil
}

// pushKey pushes publicKey to instanceID for ec2InstanceConnectUser via EC2
// Instance Connect. The key is valid for 60 seconds, scoped to that one
// instance + OS user.
func (c *ec2Connector) pushKey(ctx context.Context, res Resource, instanceID, publicKey string) error {
	cfg, err := c.registry.loadAWSConfig(ctx, res.CloudAccountID, res.Region)
	if err != nil {
		return err
	}
	client := ec2instanceconnect.NewFromConfig(cfg)
	if _, err := client.SendSSHPublicKey(ctx, &ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId:     aws.String(instanceID),
		InstanceOSUser: aws.String(ec2InstanceConnectUser),
		SSHPublicKey:   aws.String(publicKey),
	}); err != nil {
		return fmt.Errorf("console: ec2 instance connect send public key: %w", err)
	}
	return nil
}

func (c *ec2Connector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	instanceID, publicIP, err := c.target(res)
	if err != nil {
		return nil, err
	}
	signer, pub, err := ephemeralKeyPair()
	if err != nil {
		return nil, err
	}
	if err := c.pushKey(ctx, res, instanceID, pub); err != nil {
		return nil, err
	}
	return dialSSHShell(ctx, publicIP+":22", ec2InstanceConnectUser, signer, rows, cols)
}

func (c *ec2Connector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	instanceID, publicIP, err := c.target(res)
	if err != nil {
		return ExecResult{}, err
	}
	signer, pub, err := ephemeralKeyPair()
	if err != nil {
		return ExecResult{}, err
	}
	if err := c.pushKey(ctx, res, instanceID, pub); err != nil {
		return ExecResult{}, err
	}
	return sshExec(ctx, publicIP+":22", ec2InstanceConnectUser, signer, command)
}
