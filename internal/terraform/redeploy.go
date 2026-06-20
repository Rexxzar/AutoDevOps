package terraform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

type registryCredential struct {
	Server   string
	Username string
	Password string
}

// RedeployACIImage swaps the container image (and environment variables) on
// an already-applied Azure Container Instance group, re-planning and
// re-applying the workspace so terraform's state stays authoritative — ACI
// container groups are immutable, so changing the image forces terraform to
// replace azurerm_container_group.main. resourceID is the
// provisioned_resources.id of the "aci" resource (deploymentID is empty, per
// the standalone-Resource workspace convention). Returns the Apply run's
// outputs so the caller can refresh provisioned_resources.outputs.
func (r *Runner) RedeployACIImage(ctx context.Context, resourceID, cloudAccountID, image string, env map[string]string, regServer, regUser, regPass string) (map[string]json.RawMessage, error) {
	cfg, err := r.ReadConfig("", resourceID)
	if err != nil {
		return nil, fmt.Errorf("terraform: read config for %s: %w", resourceID, err)
	}

	var regCred *registryCredential
	if regServer != "" {
		regCred = &registryCredential{Server: regServer, Username: regUser, Password: regPass}
	}
	updated, err := setACIContainerImage(cfg, image, env, regCred)
	if err != nil {
		return nil, fmt.Errorf("terraform: %s: %w", resourceID, err)
	}

	if _, err := r.Plan(ctx, "", resourceID, cloudAccountID, string(AzureStackID), updated); err != nil {
		return nil, err
	}
	run, err := r.Apply(ctx, "", resourceID, cloudAccountID, string(AzureStackID))
	if err != nil {
		return nil, err
	}
	return run.Outputs, nil
}

// setACIContainerImage rewrites azurerm_container_group.main's container
// block in cfg (a previously-rendered main.tf) to set image and
// environment_variables, returning the re-serialized HCL. A nil/empty env
// removes the environment_variables attribute entirely rather than setting
// an empty object.
func setACIContainerImage(cfg []byte, image string, env map[string]string, regCred *registryCredential) ([]byte, error) {
	f, diags := hclwrite.ParseConfig(cfg, "main.tf", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse config: %s", diags.Error())
	}

	group := f.Body().FirstMatchingBlock("resource", []string{"azurerm_container_group", "main"})
	if group == nil {
		return nil, fmt.Errorf("azurerm_container_group.main not found in rendered config")
	}

	var container *hclwrite.Block
	for _, block := range group.Body().Blocks() {
		if block.Type() == "container" {
			container = block
			break
		}
	}
	if container == nil {
		return nil, fmt.Errorf("azurerm_container_group.main has no container block")
	}

	container.Body().SetAttributeValue("image", cty.StringVal(image))

	envValues := make(map[string]cty.Value, len(env))
	for k, v := range env {
		envValues[k] = cty.StringVal(v)
	}
	if len(envValues) > 0 {
		container.Body().SetAttributeValue("environment_variables", cty.ObjectVal(envValues))
	} else {
		container.Body().RemoveAttribute("environment_variables")
	}

	if regCred != nil {
		for _, block := range group.Body().Blocks() {
			if block.Type() == "image_registry_credential" {
				group.Body().RemoveBlock(block)
				break
			}
		}
		credBlock := group.Body().AppendNewBlock("image_registry_credential", nil)
		credBlock.Body().SetAttributeValue("server", cty.StringVal(regCred.Server))
		credBlock.Body().SetAttributeValue("username", cty.StringVal(regCred.Username))
		credBlock.Body().SetAttributeValue("password", cty.StringVal(regCred.Password))
	}

	return f.Bytes(), nil
}
