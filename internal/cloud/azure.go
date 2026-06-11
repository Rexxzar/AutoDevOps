package cloud

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v2"
)

// azureProvider lists every resource type AutoDevOps can provision for one
// Azure subscription — Virtual Machines, Container Instances (ACI), and
// PostgreSQL Flexible Server databases — using a credential decrypted from
// the secrets vault — either a service-principal (tenant_id/client_id/
// client_secret, vault-encrypted, suitable for production) or, when those
// are omitted, the operator's own `az login` session via AzureCLICredential
// (suitable for local dev/demo, e.g. institutional tenants that block Entra
// ID app registration for students). Mirrors the fallback ordering of the
// SDK's DefaultAzureCredential chain.
type azureProvider struct {
	cloudAccountID  string
	vmClient        *armcompute.VirtualMachinesClient
	containerClient *armcontainerinstance.ContainerGroupsClient
	postgresClient  *armpostgresqlflexibleservers.ServersClient
	vnetClient      *armnetwork.VirtualNetworksClient
}

func newAzureProvider(cloudAccountID string, creds azureCredentials) (*azureProvider, error) {
	if creds.SubscriptionID == "" {
		return nil, fmt.Errorf("cloud: azure credentials require subscription_id")
	}

	var cred azcore.TokenCredential
	var err error
	switch {
	case creds.ClientID != "" || creds.ClientSecret != "" || creds.TenantID != "":
		if creds.TenantID == "" || creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, fmt.Errorf("cloud: azure service-principal credentials require tenant_id, client_id and client_secret")
		}
		cred, err = azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	default:
		cred, err = azidentity.NewAzureCLICredential(nil)
	}
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure credential: %w", err)
	}

	vmClient, err := armcompute.NewVirtualMachinesClient(creds.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure compute client: %w", err)
	}
	containerClient, err := armcontainerinstance.NewContainerGroupsClient(creds.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure container instance client: %w", err)
	}
	postgresClient, err := armpostgresqlflexibleservers.NewServersClient(creds.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure postgresql flexible server client: %w", err)
	}
	vnetClient, err := armnetwork.NewVirtualNetworksClient(creds.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build azure virtual networks client: %w", err)
	}

	return &azureProvider{
		cloudAccountID:  cloudAccountID,
		vmClient:        vmClient,
		containerClient: containerClient,
		postgresClient:  postgresClient,
		vnetClient:      vnetClient,
	}, nil
}

// ListResources lists every Virtual Machine, Container Instance (ACI), and
// PostgreSQL Flexible Server in the subscription — the Azure-side resource
// types AutoDevOps can provision via templates/azure/stack.tf.tmpl (VM, ACI,
// DB), so the dashboard's Cloud Inventory mirrors what the wizard offers.
func (p *azureProvider) ListResources(ctx context.Context) ([]Resource, error) {
	var resources []Resource

	vms, err := p.listVirtualMachines(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, vms...)

	containers, err := p.listContainerGroups(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, containers...)

	dbs, err := p.listPostgresServers(ctx)
	if err != nil {
		return nil, err
	}
	resources = append(resources, dbs...)

	return resources, nil
}

func (p *azureProvider) listVirtualMachines(ctx context.Context) ([]Resource, error) {
	var resources []Resource
	pager := p.vmClient.NewListAllPager(nil)
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

func (p *azureProvider) listContainerGroups(ctx context.Context) ([]Resource, error) {
	var resources []Resource
	pager := p.containerClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: azure list container groups: %w", err)
		}
		for _, cg := range page.Value {
			resources = append(resources, containerGroupToResource(p.cloudAccountID, cg))
		}
	}
	return resources, nil
}

func (p *azureProvider) listPostgresServers(ctx context.Context) ([]Resource, error) {
	var resources []Resource
	pager := p.postgresClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: azure list postgresql flexible servers: %w", err)
		}
		for _, srv := range page.Value {
			resources = append(resources, postgresServerToResource(p.cloudAccountID, srv))
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
		state["tags"] = derefStringMap(vm.Tags)
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

// containerGroupToResource maps one Azure SDK container group (ACI) into the
// provider-agnostic Resource shape. Pure mapping, no SDK calls — exercised
// directly in tests with synthetic *armcontainerinstance.ContainerGroup
// values (no network/credentials needed).
func containerGroupToResource(cloudAccountID string, cg *armcontainerinstance.ContainerGroup) Resource {
	if cg == nil {
		return Resource{CloudAccountID: cloudAccountID, Provider: ProviderAzure, ResourceType: ResourceTypeContainerInstance}
	}

	state := map[string]any{}
	if cg.Properties != nil {
		if cg.Properties.ProvisioningState != nil {
			state["provisioning_state"] = derefString(cg.Properties.ProvisioningState)
		}
		if cg.Properties.InstanceView != nil && cg.Properties.InstanceView.State != nil {
			state["state"] = derefString(cg.Properties.InstanceView.State)
		}
		if cg.Properties.OSType != nil {
			state["os_type"] = string(*cg.Properties.OSType)
		}
		if cg.Properties.IPAddress != nil {
			if cg.Properties.IPAddress.Fqdn != nil {
				state["fqdn"] = derefString(cg.Properties.IPAddress.Fqdn)
			}
			if cg.Properties.IPAddress.IP != nil {
				state["ip"] = derefString(cg.Properties.IPAddress.IP)
			}
		}
		if len(cg.Properties.Containers) > 0 && cg.Properties.Containers[0].Properties != nil {
			state["image"] = derefString(cg.Properties.Containers[0].Properties.Image)
		}
	}
	if len(cg.Tags) > 0 {
		state["tags"] = derefStringMap(cg.Tags)
	}

	return Resource{
		CloudAccountID: cloudAccountID,
		Provider:       ProviderAzure,
		ResourceType:   ResourceTypeContainerInstance,
		ResourceID:     derefString(cg.ID),
		Region:         derefString(cg.Location),
		Name:           derefString(cg.Name),
		State:          state,
	}
}

// postgresServerToResource maps one Azure SDK PostgreSQL Flexible Server
// into the provider-agnostic Resource shape. Pure mapping, no SDK calls —
// exercised directly in tests with synthetic
// *armpostgresqlflexibleservers.Server values (no network/credentials needed).
func postgresServerToResource(cloudAccountID string, srv *armpostgresqlflexibleservers.Server) Resource {
	if srv == nil {
		return Resource{CloudAccountID: cloudAccountID, Provider: ProviderAzure, ResourceType: ResourceTypePostgresFlexibleServer}
	}

	state := map[string]any{}
	if srv.Properties != nil {
		if srv.Properties.State != nil {
			state["state"] = string(*srv.Properties.State)
		}
		if srv.Properties.Version != nil {
			state["version"] = string(*srv.Properties.Version)
		}
		if srv.Properties.FullyQualifiedDomainName != nil {
			state["fqdn"] = derefString(srv.Properties.FullyQualifiedDomainName)
		}
	}
	if srv.SKU != nil && srv.SKU.Name != nil {
		state["sku"] = derefString(srv.SKU.Name)
	}
	if len(srv.Tags) > 0 {
		state["tags"] = derefStringMap(srv.Tags)
	}

	return Resource{
		CloudAccountID: cloudAccountID,
		Provider:       ProviderAzure,
		ResourceType:   ResourceTypePostgresFlexibleServer,
		ResourceID:     derefString(srv.ID),
		Region:         derefString(srv.Location),
		Name:           derefString(srv.Name),
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

// derefStringMap safely dereferences an Azure SDK tags map (map[string]*string).
func derefStringMap(m map[string]*string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = derefString(v)
	}
	return out
}
