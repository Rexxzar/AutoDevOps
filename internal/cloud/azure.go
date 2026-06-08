package cloud

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
)

// azureProvider lists Virtual Machines for one Azure subscription using a
// service-principal credential decrypted from the secrets vault.
type azureProvider struct {
	cloudAccountID string
	client         *armcompute.VirtualMachinesClient
}

func newAzureProvider(cloudAccountID string, creds azureCredentials) (Provider, error) {
	if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" || creds.SubscriptionID == "" {
		return nil, fmt.Errorf("cloud: azure credentials require tenant_id, client_id, client_secret and subscription_id")
	}

	cred, err := azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure credential: %w", err)
	}

	client, err := armcompute.NewVirtualMachinesClient(creds.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure compute client: %w", err)
	}

	return &azureProvider{cloudAccountID: cloudAccountID, client: client}, nil
}

func (p *azureProvider) ListResources(ctx context.Context) ([]Resource, error) {
	var resources []Resource

	pager := p.client.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: azure list virtual machines: %w", err)
		}
		for _, vm := range page.Value {
			resources = append(resources, virtualMachineToResource(p.cloudAccountID, vm))
		}
	}
	return resources, nil
}

// virtualMachineToResource maps one Azure SDK VM into the provider-agnostic
// Resource shape. Pure mapping, no SDK calls — exercised directly in tests
// with synthetic *armcompute.VirtualMachine values (no network/credentials needed).
func virtualMachineToResource(cloudAccountID string, vm *armcompute.VirtualMachine) Resource {
	if vm == nil {
		return Resource{CloudAccountID: cloudAccountID, Provider: ProviderAzure, ResourceType: ResourceTypeVirtualMachine}
	}

	state := map[string]any{}
	if vm.Properties != nil {
		if vm.Properties.ProvisioningState != nil {
			state["provisioning_state"] = derefString(vm.Properties.ProvisioningState)
		}
		if vm.Properties.HardwareProfile != nil && vm.Properties.HardwareProfile.VMSize != nil {
			state["vm_size"] = string(*vm.Properties.HardwareProfile.VMSize)
		}
	}
	if len(vm.Tags) > 0 {
		tags := make(map[string]string, len(vm.Tags))
		for k, v := range vm.Tags {
			tags[k] = derefString(v)
		}
		state["tags"] = tags
	}

	return Resource{
		CloudAccountID: cloudAccountID,
		Provider:       ProviderAzure,
		ResourceType:   ResourceTypeVirtualMachine,
		ResourceID:     derefString(vm.ID),
		Region:         derefString(vm.Location),
		Name:           derefString(vm.Name),
		State:          state,
	}
}

// derefString safely dereferences an Azure SDK *string field — the Azure SDK
// has no equivalent of aws.ToString for nil-safe pointer access.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
