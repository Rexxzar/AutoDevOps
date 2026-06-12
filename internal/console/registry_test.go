package console

import "testing"

// TestRegistryConnectorDispatch verifies the (provider, resource_type)
// dispatch table. provisioned_resources.resource_type is "vm" for both AWS
// EC2 and Azure VMs (see the design notes: Dev Notes: "Resource Console design"), so
// provider must disambiguate. Connector() itself never touches the pool/vault
// (only the connectors' Open/Exec do), so nil dependencies are safe here.
func TestRegistryConnectorDispatch(t *testing.T) {
	reg := NewRegistry(nil, nil)

	tests := []struct {
		provider, resourceType string
		want                    bool
	}{
		{"aws", "vm", true},
		{"azure", "vm", true},
		{"aws", "rds", false},
		{"aws", "ecs", false},
		{"azure", "aci", false},
		{"azure", "db", false},
		{"gcp", "vm", false},
	}
	for _, tt := range tests {
		_, ok := reg.Connector(tt.provider, tt.resourceType)
		if ok != tt.want {
			t.Errorf("Connector(%q, %q) ok = %v, want %v", tt.provider, tt.resourceType, ok, tt.want)
		}
	}
}
