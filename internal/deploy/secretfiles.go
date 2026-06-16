package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// loadSecretFiles decrypts secretFilesID (deployment_apps.secret_files_id,
// may be nil) into a container-path -> file-content map ready for
// KubeDeploy(Image)App/DockerDeploy(Image)App. The vault blob is JSON
// {containerPath: base64Content}; values are base64-decoded here so
// deployers receive raw bytes. Returns nil (not an error) when
// secretFilesID is nil.
func (o *Orchestrator) loadSecretFiles(ctx context.Context, secretFilesID *string) (map[string][]byte, error) {
	if secretFilesID == nil {
		return nil, nil
	}
	plaintext, err := o.vault.GetByID(ctx, *secretFilesID)
	if err != nil {
		return nil, fmt.Errorf("deploy: decrypt secret files: %w", err)
	}
	var encoded map[string]string
	if err := json.Unmarshal(plaintext, &encoded); err != nil {
		return nil, fmt.Errorf("deploy: decode secret files: %w", err)
	}
	files := make(map[string][]byte, len(encoded))
	for path, b64 := range encoded {
		content, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("deploy: decode secret file %q: %w", path, err)
		}
		files[path] = content
	}
	return files, nil
}
