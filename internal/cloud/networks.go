package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Network is the provider-agnostic shape for an existing VPC (AWS) or VNet
// (Azure) and its subnets, returned by ListNetworks for the New Resource
// wizard's "use existing network" picker (see the design notes: Terraform engine
// scope notes — existing-resource references are wired as `data` blocks
// backed by these IDs, not remote state).
type Network struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	CIDR    string   `json:"cidr,omitempty"`
	Subnets []Subnet `json:"subnets"`

	// ResourceGroup is the Azure resource group the VNet lives in, parsed
	// from its ARM resource ID — possibly different from the deployment's
	// own resource group. Empty for AWS VPCs. Used by the "use existing
	// VNet" picker, which needs name+resource-group (not the ARM ID) to
	// populate AzureExistingVNet.
	ResourceGroup string `json:"resource_group,omitempty"`
}

// Subnet is one subnet within a Network.
type Subnet struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	CIDR             string `json:"cidr,omitempty"`
	AvailabilityZone string `json:"availability_zone,omitempty"`
}

// ListNetworks looks up cloudAccountID's provider and decrypts its vault
// credentials, then lists existing VPCs/VNets and their subnets in region —
// backs GET /api/cloud-accounts/{id}/networks. Mirrors the credential-decode
// dispatch in Poller.pollAccount, but is called on demand from the New
// Resource wizard rather than on the polling ticker.
func ListNetworks(ctx context.Context, pool *pgxpool.Pool, vault *secrets.Vault, cloudAccountID, region string) ([]Network, error) {
	var provider, secretID string
	if err := pool.QueryRow(ctx,
		"SELECT provider, credentials_secret_id FROM cloud_accounts WHERE id = $1", cloudAccountID,
	).Scan(&provider, &secretID); err != nil {
		return nil, fmt.Errorf("cloud: look up cloud account %s: %w", cloudAccountID, err)
	}

	plaintext, err := vault.GetByID(ctx, secretID)
	if err != nil {
		return nil, fmt.Errorf("cloud: decrypt cloud account %s credentials: %w", cloudAccountID, err)
	}

	switch provider {
	case ProviderAWS:
		var creds awsCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return nil, fmt.Errorf("cloud: decode aws credentials: %w", err)
		}
		prov, err := newAWSProvider(ctx, cloudAccountID, region, creds)
		if err != nil {
			return nil, err
		}
		return prov.ListNetworks(ctx)
	case ProviderAzure:
		var creds azureCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			return nil, fmt.Errorf("cloud: decode azure credentials: %w", err)
		}
		prov, err := newAzureProvider(cloudAccountID, creds)
		if err != nil {
			return nil, err
		}
		return prov.ListNetworks(ctx, region)
	default:
		return nil, fmt.Errorf("cloud: unknown provider %q", provider)
	}
}

// ListNetworks lists every VPC and its subnets in the provider's region.
func (p *awsProvider) ListNetworks(ctx context.Context) ([]Network, error) {
	out, err := p.ec2Client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	if err != nil {
		return nil, fmt.Errorf("cloud: aws describe vpcs: %w", err)
	}

	networks := make([]Network, 0, len(out.Vpcs))
	for _, vpc := range out.Vpcs {
		net := Network{
			ID:   aws.ToString(vpc.VpcId),
			Name: ec2TagValue(vpc.Tags, "Name"),
			CIDR: aws.ToString(vpc.CidrBlock),
		}

		subnetsOut, err := p.ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
			Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{net.ID}}},
		})
		if err != nil {
			return nil, fmt.Errorf("cloud: aws describe subnets for %s: %w", net.ID, err)
		}
		for _, sn := range subnetsOut.Subnets {
			net.Subnets = append(net.Subnets, Subnet{
				ID:               aws.ToString(sn.SubnetId),
				Name:             ec2TagValue(sn.Tags, "Name"),
				CIDR:             aws.ToString(sn.CidrBlock),
				AvailabilityZone: aws.ToString(sn.AvailabilityZone),
			})
		}
		networks = append(networks, net)
	}
	return networks, nil
}

// ec2TagValue returns the value of the named tag, or "" if absent.
func ec2TagValue(tags []ec2types.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

// ListNetworks lists every VNet (and its subnets) in region across the
// subscription — armnetwork has no per-region list call, so this fetches
// every VNet and filters by Location client-side.
func (p *azureProvider) ListNetworks(ctx context.Context, region string) ([]Network, error) {
	var networks []Network
	pager := p.vnetClient.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cloud: azure list virtual networks: %w", err)
		}
		for _, vnet := range page.Value {
			if vnet == nil || derefString(vnet.Location) != region {
				continue
			}
			networks = append(networks, virtualNetworkToNetwork(vnet))
		}
	}
	return networks, nil
}

// virtualNetworkToNetwork maps one Azure SDK virtual network into the
// provider-agnostic Network shape. Pure mapping, no SDK calls — exercised
// directly in tests with synthetic *armnetwork.VirtualNetwork values (no
// network/credentials needed).
func virtualNetworkToNetwork(vnet *armnetwork.VirtualNetwork) Network {
	net := Network{
		ID:   derefString(vnet.ID),
		Name: derefString(vnet.Name),
	}
	if id, err := arm.ParseResourceID(net.ID); err == nil {
		net.ResourceGroup = id.ResourceGroupName
	}
	if vnet.Properties == nil {
		return net
	}
	if vnet.Properties.AddressSpace != nil && len(vnet.Properties.AddressSpace.AddressPrefixes) > 0 {
		net.CIDR = derefString(vnet.Properties.AddressSpace.AddressPrefixes[0])
	}
	for _, sn := range vnet.Properties.Subnets {
		if sn == nil {
			continue
		}
		subnet := Subnet{ID: derefString(sn.ID), Name: derefString(sn.Name)}
		if sn.Properties != nil {
			subnet.CIDR = derefString(sn.Properties.AddressPrefix)
		}
		net.Subnets = append(net.Subnets, subnet)
	}
	return net
}
