package cloud

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestEC2TagValue(t *testing.T) {
	tags := []ec2types.Tag{
		{Key: strPtr("Environment"), Value: strPtr("staging")},
		{Key: strPtr("Name"), Value: strPtr("vpc-main")},
	}

	if got := ec2TagValue(tags, "Name"); got != "vpc-main" {
		t.Errorf("ec2TagValue(Name) = %q, want %q", got, "vpc-main")
	}
	if got := ec2TagValue(tags, "Missing"); got != "" {
		t.Errorf("ec2TagValue(Missing) = %q, want empty string", got)
	}
	if got := ec2TagValue(nil, "Name"); got != "" {
		t.Errorf("ec2TagValue(nil tags) = %q, want empty string", got)
	}
}

func TestVirtualNetworkToNetwork(t *testing.T) {
	vnet := &armnetwork.VirtualNetwork{
		ID:       to.Ptr("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet-1"),
		Name:     to.Ptr("vnet-1"),
		Location: to.Ptr("germanywestcentral"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{to.Ptr("10.0.0.0/16")},
			},
			Subnets: []*armnetwork.Subnet{
				{
					ID:   to.Ptr("/subscriptions/sub/.../subnets/subnet-1"),
					Name: to.Ptr("subnet-1"),
					Properties: &armnetwork.SubnetPropertiesFormat{
						AddressPrefix: to.Ptr("10.0.1.0/24"),
					},
				},
				nil,
			},
		},
	}

	net := virtualNetworkToNetwork(vnet)

	if net.ID != *vnet.ID || net.Name != "vnet-1" {
		t.Fatalf("unexpected identity fields: %+v", net)
	}
	if net.ResourceGroup != "rg" {
		t.Errorf("resource group = %q, want %q", net.ResourceGroup, "rg")
	}
	if net.CIDR != "10.0.0.0/16" {
		t.Errorf("cidr = %q, want %q", net.CIDR, "10.0.0.0/16")
	}
	if len(net.Subnets) != 1 {
		t.Fatalf("subnets = %+v, want exactly 1 (nil entries skipped)", net.Subnets)
	}
	if net.Subnets[0].Name != "subnet-1" || net.Subnets[0].CIDR != "10.0.1.0/24" {
		t.Errorf("subnet = %+v, want name=subnet-1 cidr=10.0.1.0/24", net.Subnets[0])
	}
}

func TestVirtualNetworkToNetwork_NilProperties(t *testing.T) {
	vnet := &armnetwork.VirtualNetwork{
		ID:   to.Ptr("/vnet-bare"),
		Name: to.Ptr("vnet-bare"),
	}

	net := virtualNetworkToNetwork(vnet)

	if net.ID != "/vnet-bare" || net.Name != "vnet-bare" {
		t.Fatalf("unexpected identity fields: %+v", net)
	}
	if net.ResourceGroup != "" {
		t.Errorf("expected empty resource group for unparseable ID, got %q", net.ResourceGroup)
	}
	if net.CIDR != "" || len(net.Subnets) != 0 {
		t.Errorf("expected empty cidr/subnets when Properties is nil, got %+v", net)
	}
}
