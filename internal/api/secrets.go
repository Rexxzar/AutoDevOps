package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// secretNameParam returns the {name} path param, URL-decoded. chi hands back the
// raw (un-decoded) path segment, so a name the client percent-encoded (e.g.
// "git-credentials%3A...%3Aapp" from encodeURIComponent) arrives with literal
// "%3A" and would never match the stored name. Decoding here fixes GET/DELETE
// for the colon-bearing names every platform-managed secret uses. It is a no-op
// for names sent with raw colons (no percent-escapes to decode).
func secretNameParam(r *http.Request) string {
	name := chi.URLParam(r, "name")
	if decoded, err := url.PathUnescape(name); err == nil {
		return decoded
	}
	return name
}

// secretSummary is the metadata view of a vault secret for the Secrets page.
// It deliberately never carries the decrypted value — the listing must not
// decrypt anything (consistent with the "never decrypt for diagnostics"
// boundary). The category/scope are derived from the secret's name.
type secretSummary struct {
	Name         string    `json:"name"`
	Category     string    `json:"category"`
	ScopeLabel   string    `json:"scope_label,omitempty"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	RepoID       string    `json:"repo_id,omitempty"`
	Managed      bool      `json:"managed"` // platform-managed vs user-created
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// categorizeSecret maps a vault secret name to a category, a human-readable
// scope label, and (when applicable) the owning deployment/repo id for linking.
// Naming conventions (see internal/api/handlers.go, repos.go):
//
//	webhook:<depID>:<appName>
//	git-credentials:<depID>:<appName>
//	image-env:<depID>:<appName>
//	secret-files:<depID>:<appName>
//	cloud-account:<name>
//	git-repo:<repoID>
//	<anything else>            -> user-created
func categorizeSecret(name string, deployments, repos map[string]string) (category, scope, depID, repoID string, managed bool) {
	depScoped := func(cat, label, rest string) (string, string, string, string, bool) {
		// rest = "<depID>:<appName>"
		parts := strings.SplitN(rest, ":", 2)
		id := parts[0]
		app := ""
		if len(parts) == 2 {
			app = parts[1]
		}
		depName := deployments[id]
		if depName == "" {
			depName = "(deleted deployment)"
		}
		s := depName
		if app != "" {
			s = app + " · " + depName
		}
		return cat, label + ": " + s, id, "", true
	}

	switch {
	case strings.HasPrefix(name, "webhook:"):
		return depScoped("webhook", "App webhook", strings.TrimPrefix(name, "webhook:"))
	case strings.HasPrefix(name, "git-credentials:"):
		return depScoped("git-credentials", "App git credentials", strings.TrimPrefix(name, "git-credentials:"))
	case strings.HasPrefix(name, "image-env:"):
		return depScoped("image-env", "App image env", strings.TrimPrefix(name, "image-env:"))
	case strings.HasPrefix(name, "secret-files:"):
		return depScoped("secret-files", "App secret files", strings.TrimPrefix(name, "secret-files:"))
	case strings.HasPrefix(name, "cloud-account:"):
		return "cloud-account", "Cloud account: " + strings.TrimPrefix(name, "cloud-account:"), "", "", true
	case strings.HasPrefix(name, "git-repo:"):
		id := strings.TrimPrefix(name, "git-repo:")
		repoName := repos[id]
		if repoName == "" {
			repoName = "(deleted repo)"
		}
		return "git-repo", "Repo: " + repoName, "", id, true
	default:
		return "user", "", "", "", false
	}
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Lookup maps for friendly scope labels / links.
	deployments := map[string]string{}
	if rows, err := s.db.Query(ctx, "SELECT id::text, name FROM deployments"); err == nil {
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err == nil {
				deployments[id] = name
			}
		}
		rows.Close()
	}
	repos := map[string]string{}
	if rows, err := s.db.Query(ctx, "SELECT id::text, name FROM git_repos"); err == nil {
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err == nil {
				repos[id] = name
			}
		}
		rows.Close()
	}

	rows, err := s.db.Query(ctx,
		"SELECT name, created_at, updated_at FROM secrets ORDER BY name")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list secrets")
		return
	}
	defer rows.Close()

	out := []secretSummary{}
	for rows.Next() {
		var sm secretSummary
		if err := rows.Scan(&sm.Name, &sm.CreatedAt, &sm.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read secrets")
			return
		}
		sm.Category, sm.ScopeLabel, sm.DeploymentID, sm.RepoID, sm.Managed =
			categorizeSecret(sm.Name, deployments, repos)
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read secrets")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleVaultStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.vault.KeyStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read vault status")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleRotateDataKey(w http.ResponseWriter, r *http.Request) {
	version, count, err := s.vault.RotateDataKey(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate data key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     version,
		"reencrypted": count,
	})
}

func (s *Server) handleRotateMasterKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewMasterKey string `json:"new_master_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.NewMasterKey) == "" {
		writeError(w, http.StatusBadRequest, "new_master_key required")
		return
	}
	if err := s.vault.RotateMasterKey(r.Context(), req.NewMasterKey); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate master key")
		return
	}
	// The DEK is now wrapped under the new KEK. The operator MUST update the
	// MASTER_KEY env var to this value before the next restart, or the vault
	// will fail to unwrap at boot.
	writeJSON(w, http.StatusOK, map[string]any{
		"rotated": true,
		"warning": "Update the MASTER_KEY environment variable to the new value before restarting the server, or the vault will fail to start.",
	})
}
