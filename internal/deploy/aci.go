package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
)

// aciResource is the subset of a provisioned_resources row needed to redeploy
// a git app onto an Azure Container Instance group — duplicated from
// ecsResource/cloudVMResource per the internal/ package-boundary rule
// (internal/deploy must not import internal/console).
type aciResource struct {
	ID             string
	Name           string
	CloudAccountID string
	Outputs        map[string]any
}

// loadACIResource reads the provisioned_resources row identified by
// resourceID — a deployment_apps.target_resource_id for a cloud_aci app —
// and validates it is an applied "aci" resource before deployToACI runs. If
// the resource has a linked "acr" registry resource (registry_resource_id,
// migration 020), that resource's acr_login_server/resource_group_name
// outputs are merged into res.Outputs so deployToACI/acrAuth can push there.
func (o *Orchestrator) loadACIResource(ctx context.Context, resourceID string) (aciResource, error) {
	var res aciResource
	var resourceType, status, outputsJSON, registryResourceID string
	err := o.pool.QueryRow(ctx, `
		SELECT id::text, name, resource_type, COALESCE(cloud_account_id::text, ''), status, COALESCE(outputs::text, '{}'),
		       COALESCE(registry_resource_id::text, '')
		FROM provisioned_resources WHERE id = $1
	`, resourceID).Scan(&res.ID, &res.Name, &resourceType, &res.CloudAccountID, &status, &outputsJSON, &registryResourceID)
	if err != nil {
		return aciResource{}, fmt.Errorf("deploy: look up target resource %s: %w", resourceID, err)
	}
	if resourceType != "aci" {
		return aciResource{}, fmt.Errorf("deploy: target resource %s is not an aci container group (resource_type=%s)", resourceID, resourceType)
	}
	if status != "applied" {
		return aciResource{}, fmt.Errorf("deploy: target resource %s is not applied (status=%s)", resourceID, status)
	}
	if err := json.Unmarshal([]byte(outputsJSON), &res.Outputs); err != nil {
		return aciResource{}, fmt.Errorf("deploy: decode outputs for resource %s: %w", resourceID, err)
	}
	if registryResourceID != "" {
		registryOutputs, err := o.loadRegistryOutputs(ctx, registryResourceID, "acr")
		if err != nil {
			return aciResource{}, err
		}
		res.Outputs["acr_login_server"] = registryOutputs["acr_login_server"]
		res.Outputs["resource_group_name"] = registryOutputs["resource_group_name"]
	}
	return res, nil
}

// deployToACI ships pushRef — an image buildImage already built into the
// local Docker daemon — to res: retag and push it to the deployment's bundled
// ACR registry, then re-apply the resource's terraform workspace via HCL
// surgery so azurerm_container_group.main's container runs the new image.
// ACI container groups are immutable, so terraform replaces the group; the
// refreshed outputs from that apply are persisted back onto res so the
// resource detail page reflects the (likely unchanged) IP/FQDN.
func (o *Orchestrator) deployToACI(ctx context.Context, appName, pushRef string, extraEnv map[string]string, res aciResource) error {
	if o.pusher == nil {
		return fmt.Errorf("deploy: no image pusher configured (Docker daemon unavailable)")
	}
	if o.aciRedeployer == nil {
		return fmt.Errorf("deploy: no terraform runner configured (terraform binary unavailable)")
	}

	loginServer := outputString(res.Outputs, "acr_login_server")
	if loginServer == "" {
		return fmt.Errorf("deploy: resource %s has no linked ACR registry — link an applied ACR registry resource first", res.Name)
	}

	auth, err := o.acrAuth(ctx, res.CloudAccountID, res.Outputs)
	if err != nil {
		return err
	}

	acrRef := loginServer + "/" + appName + ":" + imageTag(pushRef)
	if err := o.pusher.TagImage(ctx, pushRef, acrRef); err != nil {
		return err
	}
	if err := o.pusher.PushImage(ctx, acrRef, &auth); err != nil {
		return err
	}

	env := make(map[string]string, len(extraEnv)+1)
	for k, v := range extraEnv {
		env[k] = v
	}
	env["PORT"] = strconv.Itoa(vmAppPort)

	outputs, err := o.aciRedeployer.RedeployACIImage(ctx, res.ID, res.CloudAccountID, acrRef, env)
	if err != nil {
		return fmt.Errorf("deploy: aci redeploy %s: %w", res.Name, err)
	}
	o.persistResourceOutputs(ctx, res.ID, outputs)
	return nil
}

// persistResourceOutputs writes the terraform outputs from an ACI redeploy
// back onto provisioned_resources.outputs — duplicated from
// internal/api.Server.setResourceOutputs's UPDATE per the package-boundary
// rule. Logs and continues on failure: the redeploy itself already succeeded,
// so a stale outputs column is preferable to surfacing this as a deploy error.
func (o *Orchestrator) persistResourceOutputs(ctx context.Context, resourceID string, outputs map[string]json.RawMessage) {
	data, err := json.Marshal(outputs)
	if err != nil {
		slog.Error("deploy: marshal aci outputs", "resource_id", resourceID, "error", err)
		return
	}
	if _, err := o.pool.Exec(ctx, `UPDATE provisioned_resources SET outputs = $2, updated_at = now() WHERE id = $1`, resourceID, data); err != nil {
		slog.Error("deploy: persist aci outputs", "resource_id", resourceID, "error", err)
	}
}
