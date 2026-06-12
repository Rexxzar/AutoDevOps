package console

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
)

// ec2InstanceConnectUser is the SSH login user for EC2 Instance Connect.
// stack.tf.tmpl pins the AMI to Amazon Linux 2023 (al2023-ami-*-x86_64),
// whose default user is "ec2-user" and which ships the EC2 Instance Connect
// agent preinstalled.
const ec2InstanceConnectUser = "ec2-user"

// ec2KeyPushRetries/Delay tolerate two related EC2 Instance Connect races.
// SendSSHPublicKey can return Success: false (HTTP 200, no Go error) if the
// push to instance metadata didn't take — and even a push that does succeed
// can take a moment for the instance's AuthorizedKeysCommand helper to
// recognize. Unlike Azure Run Command (which blocks via PollUntilDone until
// the key is actually written to authorized_keys), neither of these is
// guaranteed by the time SendSSHPublicKey returns, so retry the whole
// push-then-dial cycle with a fresh ephemeral key each time.
const (
	ec2KeyPushRetries = 5
	ec2KeyPushDelay   = 2 * time.Second
)

// errKeyPushNotReady indicates SendSSHPublicKey returned Success: false — the
// push itself didn't take effect, distinct from an SDK-level error (bad
// credentials, unknown instance, etc.) which isn't retryable.
var errKeyPushNotReady = errors.New("console: ec2 instance connect: key push did not succeed")

// shouldRetryKeyPush reports whether err looks like one of the transient EC2
// Instance Connect races described above, worth retrying with a fresh key
// push, as opposed to a permanent error (bad credentials, unknown instance,
// wrong username).
func shouldRetryKeyPush(err error) bool {
	return err != nil && (errors.Is(err, errKeyPushNotReady) || isAuthFailure(err))
}

// withKeyPushRetry calls fn — which should push a fresh ephemeral key and
// then dial/exec — up to ec2KeyPushRetries times, retrying with a short delay
// while shouldRetryKeyPush(err) is true.
func withKeyPushRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < ec2KeyPushRetries; attempt++ {
		err = fn()
		if !shouldRetryKeyPush(err) {
			return err
		}
		if attempt == ec2KeyPushRetries-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ec2KeyPushDelay):
		}
	}
	return err
}

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
// instance + OS user. SendSSHPublicKey can return Success: false without a Go
// error if the push didn't take — that's surfaced as errKeyPushNotReady.
func (c *ec2Connector) pushKey(ctx context.Context, res Resource, instanceID, publicKey string) error {
	cfg, err := c.registry.loadAWSConfig(ctx, res.CloudAccountID, res.Region)
	if err != nil {
		return err
	}
	client := ec2instanceconnect.NewFromConfig(cfg)
	out, err := client.SendSSHPublicKey(ctx, &ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId:     aws.String(instanceID),
		InstanceOSUser: aws.String(ec2InstanceConnectUser),
		SSHPublicKey:   aws.String(publicKey),
	})
	if err != nil {
		return fmt.Errorf("console: ec2 instance connect send public key: %w", err)
	}
	if !out.Success {
		return errKeyPushNotReady
	}
	return nil
}

func (c *ec2Connector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	instanceID, publicIP, err := c.target(res)
	if err != nil {
		return nil, err
	}

	var sess *sshPTYSession
	if err := withKeyPushRetry(ctx, func() error {
		signer, pub, err := ephemeralKeyPair()
		if err != nil {
			return err
		}
		if err := c.pushKey(ctx, res, instanceID, pub); err != nil {
			return err
		}
		var dialErr error
		sess, dialErr = dialSSHShell(ctx, publicIP+":22", ec2InstanceConnectUser, signer, rows, cols)
		return dialErr
	}); err != nil {
		return nil, err
	}
	return sess, nil
}

func (c *ec2Connector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	instanceID, publicIP, err := c.target(res)
	if err != nil {
		return ExecResult{}, err
	}

	var result ExecResult
	err = withKeyPushRetry(ctx, func() error {
		signer, pub, err := ephemeralKeyPair()
		if err != nil {
			return err
		}
		if err := c.pushKey(ctx, res, instanceID, pub); err != nil {
			return err
		}
		var execErr error
		result, execErr = sshExec(ctx, publicIP+":22", ec2InstanceConnectUser, signer, command)
		return execErr
	})
	return result, err
}
