// Package terraform renders parameterized HCL from embedded templates and
// (via terraform-exec, added in T10) plans/applies the result. All Terraform
// operations for the platform go through this package.
package terraform

// Provider identifies a supported cloud provider for generated infrastructure.
type Provider string

const (
	ProviderAWS   Provider = "aws"
	ProviderAzure Provider = "azure"
)

// TemplateID identifies a registered HCL template definition.
type TemplateID string

// Params is implemented by the typed parameter struct for a template.
// Validate returns a descriptive error for the first invalid field, so the
// CLI and dashboard can surface friendly messages before anything is rendered.
type Params interface {
	Validate() error
}

// Definition registers an embedded HCL template under a stable ID, pairing it
// with the provider it targets. The template file at Path must produce a
// single combined stack for that provider: generated infrastructure is one
// file per deployment rather than fragments.
type Definition struct {
	ID       TemplateID
	Provider Provider
	// Path is the template's location within the embedded filesystem, e.g.
	// "templates/aws/stack.tf.tmpl".
	Path string
}
