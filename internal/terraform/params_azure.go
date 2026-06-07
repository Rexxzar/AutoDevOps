package terraform

import (
	"fmt"
	"net"
	"strings"
)

// AzureStackID is the registered template for the combined Azure deployment
// stack — the Azure mirror of AWSStackID. T09 extends this file and
// templates/azure/stack.tf.tmpl with VM, DB, and ACI, following the exact
// same one-combined-stack-per-deployment + nil-able-sub-struct pattern
// proven in T08 (see params_aws.go) — keep extending this same file/struct
// rather than creating parallel ones.
const AzureStackID TemplateID = "azure-stack"

// AzureStackParams is the full desired-state parameter set for one Azure
// deployment's combined Terraform stack. A resource group and virtual
// network are always present — Azure requires every resource to live inside
// a resource group (subscription -> resource group -> resources), so there's
// no flat VPC the way AWS has. Every other resource is opt-in via a nil-able
// sub-struct, exactly like AWSStackParams: nil means the user didn't select
// it, and the template renders nothing for it (see {{ if .VM }}).
type AzureStackParams struct {
	// Name tags the generated resources and names the resource group,
	// identifying the deployment.
	Name string
	// Location is the Azure region to provision into, e.g. "uksouth".
	Location string
	// VNetCIDR is the address space for the deployment's virtual network,
	// e.g. "10.0.0.0/16".
	VNetCIDR string

	// VM provisions a single Linux virtual machine (subnet, network security
	// group, public IP, network interface, VM) wired to the deployment's
	// virtual network. Nil means no VM was selected.
	VM *AzureVMParams
	// DB provisions a managed PostgreSQL Flexible Server database,
	// authenticated exclusively via Azure AD. Nil means no database was
	// selected.
	DB *AzureDBParams
	// ACI provisions a single containerized service on Azure Container
	// Instances. Nil means no container service was selected.
	ACI *AzureACIParams
}

// Validate checks that all fields needed to render a valid Azure stack are
// present and well-formed, returning a friendly error for the first problem
// found so the CLI/dashboard can surface it before rendering.
func (p AzureStackParams) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if p.Location == "" {
		return fmt.Errorf("location is required")
	}
	if _, _, err := net.ParseCIDR(p.VNetCIDR); err != nil {
		return fmt.Errorf("vnet_cidr %q: %w", p.VNetCIDR, err)
	}
	if p.VM != nil {
		if err := p.VM.Validate(); err != nil {
			return fmt.Errorf("vm: %w", err)
		}
	}
	if p.DB != nil {
		if err := p.DB.Validate(); err != nil {
			return fmt.Errorf("db: %w", err)
		}
	}
	if p.ACI != nil {
		if err := p.ACI.Validate(); err != nil {
			return fmt.Errorf("aci: %w", err)
		}
	}
	return nil
}

// AzureVMParams describes a single Linux virtual machine and the
// subnet/network-security-group/public-IP/network-interface it runs behind.
// Authentication is SSH-key-only (disable_password_authentication = true) —
// the same no-stored-credentials principle as RDS's
// manage_master_user_password: AutoDevOps never generates, transmits, or
// stores a VM password, it only relays the user's own (non-secret) public
// key into the generated config, so nothing touches the secrets vault.
type AzureVMParams struct {
	// Size is the VM size, e.g. "Standard_B1s".
	Size string
	// SubnetCIDR is the CIDR block for the VM's subnet, e.g. "10.0.1.0/24"
	// — must fit within the stack's VNetCIDR.
	SubnetCIDR string
	// AdminUsername is the Linux admin account created on the VM.
	AdminUsername string
	// SSHPublicKey is the user's SSH public key, e.g.
	// "ssh-ed25519 AAAA... user@host" — relayed verbatim into admin_ssh_key.
	// Public keys aren't secrets, so this never goes through the vault.
	SSHPublicKey string
}

// Validate checks the VM fields are present and well-formed.
func (p AzureVMParams) Validate() error {
	if p.Size == "" {
		return fmt.Errorf("size is required")
	}
	if _, _, err := net.ParseCIDR(p.SubnetCIDR); err != nil {
		return fmt.Errorf("subnet_cidr %q: %w", p.SubnetCIDR, err)
	}
	if p.AdminUsername == "" {
		return fmt.Errorf("admin_username is required")
	}
	if !strings.HasPrefix(p.SSHPublicKey, "ssh-") {
		return fmt.Errorf("ssh_public_key must be a public key starting with \"ssh-\"")
	}
	return nil
}

// allowedAADPrincipalTypes restricts the Azure AD administrator designation
// to the principal kinds the azurerm provider documents for Flexible Server
// — mirrors the allowedRDSEngines allowlist pattern from params_aws.go.
var allowedAADPrincipalTypes = map[string]bool{"User": true, "Group": true, "ServicePrincipal": true}

// AzureDBParams describes a managed PostgreSQL Flexible Server database,
// authenticated exclusively via Azure AD (Entra ID) — the Azure-native
// equivalent of RDS's manage_master_user_password principle. Password
// authentication is disabled entirely (authentication.password_auth_enabled
// = false), so AutoDevOps never generates, transmits, or stores a database
// password; it only relays the user's own (non-secret) Azure AD identity
// into the active-directory-administrator designation — the same
// "relay, don't generate" pattern as the VM's SSH public key.
type AzureDBParams struct {
	// Version is the PostgreSQL major version, e.g. "16".
	Version string
	// SKU is the Flexible Server compute tier and size, e.g. "B_Standard_B1ms".
	SKU string
	// StorageMB is the allocated storage in MB, e.g. 32768.
	StorageMB int
	// AADAdminObjectID is the Azure AD object ID of the administrator
	// identity (user, group, or service principal) — an identifier, not a
	// secret, so it never goes through the vault.
	AADAdminObjectID string
	// AADAdminPrincipalName is the administrator's display name/UPN, e.g.
	// "alice@example.com".
	AADAdminPrincipalName string
	// AADAdminPrincipalType classifies the administrator identity — "User",
	// "Group", or "ServicePrincipal".
	AADAdminPrincipalType string
}

// Validate checks the DB fields are present, well-formed, and that the
// principal type is one the azurerm provider supports for this designation.
func (p AzureDBParams) Validate() error {
	if p.Version == "" {
		return fmt.Errorf("version is required")
	}
	if p.SKU == "" {
		return fmt.Errorf("sku is required")
	}
	if p.StorageMB <= 0 {
		return fmt.Errorf("storage_mb must be greater than zero")
	}
	if p.AADAdminObjectID == "" {
		return fmt.Errorf("aad_admin_object_id is required")
	}
	if p.AADAdminPrincipalName == "" {
		return fmt.Errorf("aad_admin_principal_name is required")
	}
	if !allowedAADPrincipalTypes[p.AADAdminPrincipalType] {
		return fmt.Errorf("aad_admin_principal_type %q: must be one of User, Group, ServicePrincipal", p.AADAdminPrincipalType)
	}
	return nil
}

// AzureACIParams describes a containerized service running on Azure
// Container Instances — a single azurerm_container_group resource exposed
// via a public IP and DNS label. This is the lightest of the three opt-in
// Azure resource types (1 resource, vs the VM's ~5 and the DB's ~4); the
// asymmetry with AWS's ~15-resource ECS/Fargate setup is itself a useful
// data point for the dissertation's cross-cloud comparison (see the ECS
// scoping note in params_aws.go).
type AzureACIParams struct {
	// ContainerImage is the image to run, e.g. "nginx:latest" or an ACR URI.
	ContainerImage string
	// ContainerPort is the port the container listens on, e.g. 80.
	ContainerPort int
	// CPU is the number of CPU cores allocated to the container, e.g. 1.0.
	CPU float64
	// MemoryGB is the memory allocated to the container in GB, e.g. 1.5.
	MemoryGB float64
}

// Validate checks the ACI fields are present and well-formed.
func (p AzureACIParams) Validate() error {
	if p.ContainerImage == "" {
		return fmt.Errorf("container_image is required")
	}
	if p.ContainerPort <= 0 || p.ContainerPort > 65535 {
		return fmt.Errorf("container_port %d: must be between 1 and 65535", p.ContainerPort)
	}
	if p.CPU <= 0 {
		return fmt.Errorf("cpu must be greater than zero")
	}
	if p.MemoryGB <= 0 {
		return fmt.Errorf("memory_gb must be greater than zero")
	}
	return nil
}
