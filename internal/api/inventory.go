package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Rexxzar/autodevops/internal/k8s"
)

// inventoryID returns the {id} chi URL param, URL-decoded. chi routes against
// the raw (percent-encoded) path when r.URL.RawPath is set, so encodeURIComponent
// on the frontend encodes the ":" separator in "docker:..." to "%3A" and chi
// stores the encoded value — we always want the decoded form for parsing.
func inventoryID(r *http.Request) string {
	raw := chi.URLParam(r, "id")
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

// parseInventoryID splits a synthetic host-inventory ID into its parts.
//
//	"docker:<containerID>"   -> ("docker", containerID, "")
//	"k8s:<namespace>:<pod>" -> ("k8s", namespace, pod)
func parseInventoryID(id string) (kind, a, b string, err error) {
	parts := strings.SplitN(id, ":", 3)
	switch parts[0] {
	case "docker":
		if len(parts) < 2 || parts[1] == "" {
			return "", "", "", fmt.Errorf("inventory: invalid docker ID %q", id)
		}
		return "docker", parts[1], "", nil
	case "k8s":
		if len(parts) < 3 || parts[1] == "" || parts[2] == "" {
			return "", "", "", fmt.Errorf("inventory: invalid k8s ID %q", id)
		}
		return "k8s", parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("inventory: unknown ID kind %q", id)
	}
}

// inventoryContainerItem is one row in the docker containers listing.
type inventoryContainerItem struct {
	ID        string `json:"id"`         // synthetic: "docker:<containerID>"
	Name      string `json:"name"`
	Image     string `json:"image"`
	State     string `json:"state"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"` // RFC3339
	Managed   bool   `json:"managed"`
	LinkPath  string `json:"link_path,omitempty"`
	LinkLabel string `json:"link_label,omitempty"`
}

// inventoryPodItem is one row in the K8s pods listing.
type inventoryPodItem struct {
	ID          string   `json:"id"`          // synthetic: "k8s:<namespace>:<pod>"
	Namespace   string   `json:"namespace"`
	Name        string   `json:"name"`
	Containers  []string `json:"containers"`  // container names in the pod
	Image       string   `json:"image"`
	Phase       string   `json:"phase"`
	CreatedAt   string   `json:"created_at"`  // RFC3339
	Managed     bool     `json:"managed"`
	Restartable bool     `json:"restartable"`
	LinkPath    string   `json:"link_path,omitempty"`
	LinkLabel   string   `json:"link_label,omitempty"`
}

type inventoryResponse struct {
	DockerAvailable bool                     `json:"docker_available"`
	K8sAvailable    bool                     `json:"k8s_available"`
	Containers      []inventoryContainerItem `json:"containers"`
	Pods            []inventoryPodItem       `json:"pods"`
}

// inventoryDeployment is a lightweight row for cross-referencing.
type inventoryDeployment struct {
	id   string
	name string
}

// inventoryApp is a lightweight row for cross-referencing.
type inventoryApp struct {
	id           string
	deploymentID string
	name         string
}

// inventoryResource is a lightweight local provisioned_resource for cross-referencing.
type inventoryResource struct {
	id   string
	name string
}

func (s *Server) loadInventoryTables(ctx context.Context) (
	depsByName map[string]inventoryDeployment,
	appsByDepID map[string][]inventoryApp,
	localResourcesByName map[string]inventoryResource,
	err error,
) {
	depsByName = make(map[string]inventoryDeployment)
	appsByDepID = make(map[string][]inventoryApp)
	localResourcesByName = make(map[string]inventoryResource)

	rows, err := s.db.Query(ctx, `SELECT id, name FROM deployments`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inventory: load deployments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d inventoryDeployment
		if err := rows.Scan(&d.id, &d.name); err != nil {
			return nil, nil, nil, fmt.Errorf("inventory: scan deployment: %w", err)
		}
		depsByName[d.name] = d
	}
	rows.Close()

	appRows, err := s.db.Query(ctx, `SELECT id, deployment_id, name FROM deployment_apps`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inventory: load apps: %w", err)
	}
	defer appRows.Close()
	for appRows.Next() {
		var a inventoryApp
		if err := appRows.Scan(&a.id, &a.deploymentID, &a.name); err != nil {
			return nil, nil, nil, fmt.Errorf("inventory: scan app: %w", err)
		}
		appsByDepID[a.deploymentID] = append(appsByDepID[a.deploymentID], a)
	}
	appRows.Close()

	resRows, err := s.db.Query(ctx, `SELECT id, name FROM provisioned_resources WHERE provider = 'local'`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inventory: load resources: %w", err)
	}
	defer resRows.Close()
	for resRows.Next() {
		var r inventoryResource
		if err := resRows.Scan(&r.id, &r.name); err != nil {
			return nil, nil, nil, fmt.Errorf("inventory: scan resource: %w", err)
		}
		localResourcesByName[r.name] = r
	}

	return depsByName, appsByDepID, localResourcesByName, nil
}

// handleLocalInventory returns a live snapshot of all Docker containers and K8s
// pods on the host, cross-referenced with AutoDevOps-managed entities.
//
// GET /local/inventory
func (s *Server) handleLocalInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	depsByName, appsByDepID, localResourcesByName, err := s.loadInventoryTables(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := inventoryResponse{
		Containers: []inventoryContainerItem{},
		Pods:       []inventoryPodItem{},
	}

	// --- Docker containers ---
	if s.dockerMgr != nil {
		resp.DockerAvailable = true
		cList, listErr := s.dockerMgr.ListContainers(ctx)
		if listErr == nil {
			for _, c := range cList {
				item := inventoryContainerItem{
					ID:        "docker:" + c.ID,
					Name:      c.Name,
					Image:     c.Image,
					State:     c.State,
					Status:    c.Status,
					CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
				}
				if c.Labels["autodevops.io/managed-by"] == "autodevops" {
					item.Managed = true
					// Case 1: standalone resource (autodevops.io/resource = resource UUID).
					if resID := c.Labels["autodevops.io/resource"]; resID != "" {
						item.LinkPath = "/local-resources/" + resID
						// Find resource name for the label.
						for _, res := range localResourcesByName {
							if res.id == resID {
								item.LinkLabel = res.name
								break
							}
						}
					} else if stackName := c.Labels["autodevops.io/stack"]; stackName != "" {
						// Case 2: deployment app.
						appName := c.Labels["autodevops.io/app"]
						if dep, ok := depsByName[stackName]; ok {
							item.LinkPath = "/deployments/" + dep.id
							item.LinkLabel = dep.name
							// Prefer app-level link if app name matches.
							for _, a := range appsByDepID[dep.id] {
								if a.name == appName {
									item.LinkPath = "/deployments/" + dep.id
									item.LinkLabel = dep.name + " / " + a.name
									break
								}
							}
						}
					}
				}
				resp.Containers = append(resp.Containers, item)
			}
		}
	}

	// --- K8s pods ---
	if s.k8sCtrl != nil {
		resp.K8sAvailable = true
		podList, listErr := s.k8sCtrl.ListPods(ctx)
		if listErr == nil {
			for _, p := range podList {
				item := inventoryPodItem{
					ID:          "k8s:" + p.Namespace + ":" + p.Name,
					Namespace:   p.Namespace,
					Name:        p.Name,
					Containers:  p.Containers,
					Image:       p.Image,
					Phase:       p.Phase,
					CreatedAt:   p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
					Restartable: p.Restartable,
				}
				if p.Labels["autodevops.io/managed-by"] == "autodevops" {
					item.Managed = true
					if strings.HasPrefix(p.Namespace, "autodevops-") {
						// Deployment app namespace.
						for depName, dep := range depsByName {
							if k8s.NamespaceName(depName) == p.Namespace {
								item.LinkPath = "/deployments/" + dep.id
								item.LinkLabel = dep.name
								appLabel := p.Labels["app"]
								for _, a := range appsByDepID[dep.id] {
									if a.name == appLabel {
										item.LinkLabel = dep.name + " / " + a.name
										break
									}
								}
								break
							}
						}
					} else if strings.HasPrefix(p.Namespace, "resource-") {
						// Standalone local K8s resource namespace.
						for resName, res := range localResourcesByName {
							if k8s.ResourceNamespace(resName) == p.Namespace {
								item.LinkPath = "/local-resources/" + res.id
								item.LinkLabel = resName
								break
							}
						}
					}
				}
				resp.Pods = append(resp.Pods, item)
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleInventoryLogs returns recent logs for a single Docker container or K8s pod.
//
// GET /local/inventory/{id}/logs?tail=300&container=
func (s *Server) handleInventoryLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := inventoryID(r)

	tail := 300
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			if n > 2000 {
				n = 2000
			}
			tail = n
		}
	}

	kind, a, b, err := parseInventoryID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var logs string
	switch kind {
	case "docker":
		if s.dockerMgr == nil {
			http.Error(w, "docker not available", http.StatusServiceUnavailable)
			return
		}
		logs, err = s.dockerMgr.ContainerLogs(ctx, a, tail)
	case "k8s":
		if s.k8sCtrl == nil {
			http.Error(w, "k8s not available", http.StatusServiceUnavailable)
			return
		}
		containerName := r.URL.Query().Get("container")
		logs, err = s.k8sCtrl.PodLogs(ctx, a, b, containerName, tail)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// handleInventoryRestart restarts a Docker container or a K8s pod.
//
// POST /local/inventory/{id}/restart
func (s *Server) handleInventoryRestart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := inventoryID(r)

	kind, a, b, err := parseInventoryID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch kind {
	case "docker":
		if s.dockerMgr == nil {
			http.Error(w, "docker not available", http.StatusServiceUnavailable)
			return
		}
		err = s.dockerMgr.RestartContainer(ctx, a)
	case "k8s":
		if s.k8sCtrl == nil {
			http.Error(w, "k8s not available", http.StatusServiceUnavailable)
			return
		}
		err = s.k8sCtrl.RestartPod(ctx, a, b)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}
