package console

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
)

// azureVMConnector opens shells / runs commands on Azure Linux VMs via VM Run
// Command: append an ephemeral SSH public key to the admin user's
// authorized_keys (executed by the Azure VM agent, as root, regardless of
// guest OS auth config) then dial SSH directly to the VM's public IP.
type azureVMConnector struct {
	registry *Registry
}

// target extracts the resource group, VM name, public IP and admin username
// needed for Run Command + SSH from the resource's terraform outputs
// (resource_group_name, vm_public_ip, vm_admin_username — stack.tf.tmpl). The
// VM's own name is provisioned_resources.name (the template names it
// azurerm_linux_virtual_machine.main = "{{ .Name }}").
func (c *azureVMConnector) target(res Resource) (rgName, vmName, publicIP, username string, err error) {
	rgName, _ = res.Outputs["resource_group_name"].(string)
	publicIP, _ = res.Outputs["vm_public_ip"].(string)
	username, _ = res.Outputs["vm_admin_username"].(string)
	vmName = res.Name
	if rgName == "" || publicIP == "" || username == "" || vmName == "" {
		return "", "", "", "", fmt.Errorf("console: resource is missing resource_group_name/vm_public_ip/vm_admin_username outputs — apply it first")
	}
	return rgName, vmName, publicIP, username, nil
}

// pushKey appends publicKey to /home/<username>/.ssh/authorized_keys via VM
// Run Command (RunShellScript). Run Command executes as root, so the path is
// written explicitly rather than relying on "~" and ownership is fixed up
// afterwards.
func (c *azureVMConnector) pushKey(ctx context.Context, res Resource, rgName, vmName, username, publicKey string) error {
	cred, subscriptionID, err := c.registry.loadAzureCredential(ctx, res.CloudAccountID)
	if err != nil {
		return err
	}
	client, err := armcompute.NewVirtualMachinesClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("console: build azure compute client: %w", err)
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
		return fmt.Errorf("console: azure run command: %w", err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("console: azure run command wait: %w", err)
	}
	return nil
}

func (c *azureVMConnector) Open(ctx context.Context, res Resource, rows, cols uint16) (Session, error) {
	rgName, vmName, publicIP, username, err := c.target(res)
	if err != nil {
		return nil, err
	}
	signer, pub, err := ephemeralKeyPair()
	if err != nil {
		return nil, err
	}
	if err := c.pushKey(ctx, res, rgName, vmName, username, pub); err != nil {
		return nil, err
	}
	return dialSSHShell(ctx, publicIP+":22", username, signer, rows, cols)
}

func (c *azureVMConnector) Exec(ctx context.Context, res Resource, command string) (ExecResult, error) {
	rgName, vmName, publicIP, username, err := c.target(res)
	if err != nil {
		return ExecResult{}, err
	}
	signer, pub, err := ephemeralKeyPair()
	if err != nil {
		return ExecResult{}, err
	}
	if err := c.pushKey(ctx, res, rgName, vmName, username, pub); err != nil {
		return ExecResult{}, err
	}
	return sshExec(ctx, publicIP+":22", username, signer, command)
}
