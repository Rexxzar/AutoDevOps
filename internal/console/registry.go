package console

// Connector returns the connector for (provider, resourceType), or false if
// this resource type has no Resource Console support. provisioned_resources.resource_type
// is "vm" for both AWS EC2 and Azure VMs (disambiguated by provider) — see
// design notes Dev Notes: "Resource Console design". RDS/DB are out of scope.
func (reg *Registry) Connector(provider, resourceType string) (Connector, bool) {
	switch provider + ":" + resourceType {
	case "aws:vm":
		return &ec2Connector{registry: reg}, true
	case "azure:vm":
		return &azureVMConnector{registry: reg}, true
	case "aws:ecs":
		return &ecsConnector{registry: reg}, true
	case "azure:aci":
		return &aciConnector{registry: reg}, true
	default:
		return nil, false
	}
}
