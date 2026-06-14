package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2instanceconnect"
	"golang.org/x/crypto/ssh"

	"github.com/Rexxzar/autodevops/internal/sshutil"
)

// cloudVMResource is the subset of a provisioned_resources row needed to SSH
// into an AWS EC2 instance or Azure Linux VM — duplicated from
// console.Resource per the internal/ package-boundary rule (internal/deploy
// must not import internal/console).
type cloudVMResource struct {
	Name           string
	Provider       string
	CloudAccountID string
	Region         string
	Outputs        map[string]any
}

// ec2InstanceConnectUser is the SSH login user for EC2 Instance Connect.
// stack.tf.tmpl pins the AMI to Amazon Linux 2023
// (al2023-ami-2023.*-kernel-*-x86_64), whose default user is "ec2-user" and
// which ships the EC2 Instance Connect agent preinstalled.
const ec2InstanceConnectUser = "ec2-user"

// vmKeyPushRetries/Delay tolerate the same EC2 Instance Connect races
// documented in internal/console/ec2.go: SendSSHPublicKey can return
// Success: false (HTTP 200, no Go error), or succeed but not yet be visible
// to the instance's AuthorizedKeysCommand helper by the time SSH dials. Retry
// the whole push-then-dial cycle with a fresh ephemeral key each time.
const (
	vmKeyPushRetries = 5
	vmKeyPushDelay   = 2 * time.Second
)

// errKeyPushNotReady indicates SendSSHPublicKey returned Success: false — the
// push itself didn't take effect, distinct from an SDK-level error (bad
// credentials, unknown instance, etc.) which isn't retryable.
var errKeyPushNotReady = errors.New("deploy: ec2 instance connect: key push did not succeed")

func shouldRetryKeyPush(err error) bool {
	return err != nil && (errors.Is(err, errKeyPushNotReady) || sshutil.IsAuthFailure(err))
}

// withKeyPushRetry calls fn — which should push a fresh ephemeral key and
// then dial — up to vmKeyPushRetries times, retrying with a short delay while
// shouldRetryKeyPush(err) is true.
func withKeyPushRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < vmKeyPushRetries; attempt++ {
		err = fn()
		if !shouldRetryKeyPush(err) {
			return err
		}
		if attempt == vmKeyPushRetries-1 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(vmKeyPushDelay):
		}
	}
	return err
}

// dialCloudVM resolves res (a provisioned_resources row with
// resource_type="vm") to an SSH connection: it generates an ephemeral
// keypair, pushes its public half via EC2 Instance Connect (aws) or VM Run
// Command (azure), then dials directly. The caller owns the returned
// *ssh.Client and must Close it.
func (o *Orchestrator) dialCloudVM(ctx context.Context, res cloudVMResource) (*ssh.Client, error) {
	switch res.Provider {
	case "aws":
		return o.dialEC2(ctx, res)
	case "azure":
		return o.dialAzureVM(ctx, res)
	default:
		return nil, fmt.Errorf("deploy: unsupported cloud_vm provider %q", res.Provider)
	}
}

// dialEC2 reads ec2_instance_id/ec2_public_ip from res.Outputs (stack.tf.tmpl)
// and dials as ec2-user, retrying the key push as needed.
func (o *Orchestrator) dialEC2(ctx context.Context, res cloudVMResource) (*ssh.Client, error) {
	instanceID, _ := res.Outputs["ec2_instance_id"].(string)
	publicIP, _ := res.Outputs["ec2_public_ip"].(string)
	if instanceID == "" || publicIP == "" {
		return nil, fmt.Errorf("deploy: resource has no ec2_instance_id/ec2_public_ip outputs — apply it first")
	}

	var client *ssh.Client
	err := withKeyPushRetry(ctx, func() error {
		signer, pub, err := sshutil.EphemeralKeyPair()
		if err != nil {
			return err
		}
		if err := o.pushEC2Key(ctx, res, instanceID, pub); err != nil {
			return err
		}
		var dialErr error
		client, dialErr = sshutil.DialContext(ctx, publicIP+":22", sshutil.ClientConfig(ec2InstanceConnectUser, signer))
		return dialErr
	})
	if err != nil {
		return nil, fmt.Errorf("deploy: ssh dial %s: %w", publicIP, err)
	}
	return client, nil
}

// pushEC2Key pushes publicKey to instanceID for ec2InstanceConnectUser via EC2
// Instance Connect. The key is valid for 60 seconds, scoped to that one
// instance + OS user. SendSSHPublicKey can return Success: false without a Go
// error if the push didn't take — that's surfaced as errKeyPushNotReady.
func (o *Orchestrator) pushEC2Key(ctx context.Context, res cloudVMResource, instanceID, publicKey string) error {
	cfg, err := o.loadAWSConfig(ctx, res.CloudAccountID, res.Region)
	if err != nil {
		return err
	}
	out, err := ec2instanceconnect.NewFromConfig(cfg).SendSSHPublicKey(ctx, &ec2instanceconnect.SendSSHPublicKeyInput{
		InstanceId:     aws.String(instanceID),
		InstanceOSUser: aws.String(ec2InstanceConnectUser),
		SSHPublicKey:   aws.String(publicKey),
	})
	if err != nil {
		return fmt.Errorf("ec2 instance connect send public key: %w", err)
	}
	if !out.Success {
		return errKeyPushNotReady
	}
	return nil
}

// dialAzureVM reads resource_group_name/vm_public_ip/vm_admin_username from
// res.Outputs (stack.tf.tmpl) plus res.Name as the VM name, pushes an
// ephemeral key via VM Run Command, and dials as the admin user.
func (o *Orchestrator) dialAzureVM(ctx context.Context, res cloudVMResource) (*ssh.Client, error) {
	rgName, _ := res.Outputs["resource_group_name"].(string)
	publicIP, _ := res.Outputs["vm_public_ip"].(string)
	username, _ := res.Outputs["vm_admin_username"].(string)
	vmName := res.Name
	if rgName == "" || publicIP == "" || username == "" || vmName == "" {
		return nil, fmt.Errorf("deploy: resource is missing resource_group_name/vm_public_ip/vm_admin_username outputs — apply it first")
	}

	signer, pub, err := sshutil.EphemeralKeyPair()
	if err != nil {
		return nil, err
	}
	if err := o.pushAzureVMKey(ctx, res, rgName, vmName, username, pub); err != nil {
		return nil, err
	}
	client, err := sshutil.DialContext(ctx, publicIP+":22", sshutil.ClientConfig(username, signer))
	if err != nil {
		return nil, fmt.Errorf("deploy: ssh dial %s: %w", publicIP, err)
	}
	return client, nil
}

// pushAzureVMKey appends publicKey to /home/<username>/.ssh/authorized_keys
// via VM Run Command (RunShellScript). Run Command executes as root, so the
// path is written explicitly rather than relying on "~" and ownership is
// fixed up afterwards.
func (o *Orchestrator) pushAzureVMKey(ctx context.Context, res cloudVMResource, rgName, vmName, username, publicKey string) error {
	cred, subscriptionID, err := o.loadAzureCredential(ctx, res.CloudAccountID)
	if err != nil {
		return err
	}
	client, err := armcompute.NewVirtualMachinesClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("build azure compute client: %w", err)
	}

	home := "/home/" + username
	script := fmt.Sprintf(
		"mkdir -p %s/.ssh && echo '%s' >> %s/.ssh/authorized_keys && chown -R %s:%s %s/.ssh && chmod 700 %s/.ssh && chmod 600 %s/.ssh/authorized_keys",
		home, publicKey, home, username, username, home, home, home,
	)

	poller, err := client.BeginRunCommand(ctx, rgName, vmName, armcompute.RunCommandInput{
		CommandID: to.Ptr("RunShellScript"),
		Script:    []*string{to.Ptr(script)},
	}, nil)
	if err != nil {
		return fmt.Errorf("azure run command: %w", err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("azure run command wait: %w", err)
	}
	return nil
}
