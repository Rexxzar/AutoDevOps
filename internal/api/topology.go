package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// topology.go backs the Topology Map page (GET /api/topology): a single
// read-only aggregation of everything AutoDevOps manages, grouped by cloud
// region, with the dependency links between apps drawn as edges. It writes
// nothing and touches no new tables — it joins provisioned_resources,
// deployment_apps, deployment_app_links and pod_health, then attaches a
// static region→lat/lon lookup so the frontend can plot pins on a world map.
//
// Node model: every provisioned_resource is a node; local_k8s / local_docker
// apps hang off two synthetic local host nodes ("local:kind" / "local:docker").
// Apps are children (chips) of the node that hosts them, never nodes of their
// own. An edge connects the node hosting the source app to the node hosting
// the depended-on app or resource, flagged cross_region when the two sit in
// different regions (the arcs that sweep across the map).

const (
	localKindNodeID   = "local:kind"
	localDockerNodeID = "local:docker"
	localRegionKey    = "local"
)

type topoApp struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"` // git | image
	Framework    string `json:"framework,omitempty"`
	TargetType   string `json:"target_type"` // k8s | container | vm | ecs | aci
	Status       string `json:"status"`
	DeploymentID string `json:"deployment_id"`
	ImageRef     string `json:"image_ref,omitempty"`
	Port         int    `json:"port,omitempty"`
	PodsReady    int    `json:"pods_ready,omitempty"`
	PodsTotal    int    `json:"pods_total,omitempty"`
}

type topoNode struct {
	ID           string    `json:"id"`   // resource UUID, or a synthetic local:* id
	Kind         string    `json:"kind"` // resource | local_cluster | docker_host
	RegionKey    string    `json:"region_key"`
	Provider     string    `json:"provider"` // aws | azure | local
	Name         string    `json:"name"`
	ResourceType string    `json:"resource_type,omitempty"`
	Status       string    `json:"status"`
	Apps         []topoApp `json:"apps"`
}

type topoEdge struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // "dependency" (app link) | "registry" (ECS/ACI → ECR/ACR)
	FromNode    string `json:"from_node"`
	ToNode      string `json:"to_node"`
	FromRegion  string `json:"from_region"`
	ToRegion    string `json:"to_region"`
	Alias       string `json:"alias,omitempty"`
	Port        int    `json:"port,omitempty"`
	CrossRegion bool   `json:"cross_region"`
}

type topoRegion struct {
	Key       string  `json:"key"`      // "aws:eu-west-1" | "local"
	Provider  string  `json:"provider"` // aws | azure | local
	Region    string  `json:"region,omitempty"`
	Label     string  `json:"label"` // human place name
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Located   bool    `json:"located"` // false when we have no coords for this region
	NodeCount int     `json:"node_count"`
}

type topologyResponse struct {
	Regions []topoRegion `json:"regions"`
	Nodes   []topoNode   `json:"nodes"`
	Edges   []topoEdge   `json:"edges"`
}

// handleTopology aggregates the whole managed estate into regions, nodes and
// edges for the Topology Map. Read-only; safe to poll and to refetch on any
// SSE event.
func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodes := map[string]*topoNode{}
	order := []string{} // preserve first-seen order, then sorted at the end

	addNode := func(n *topoNode) {
		if _, ok := nodes[n.ID]; !ok {
			order = append(order, n.ID)
		}
		nodes[n.ID] = n
	}

	// 1) Cloud/standalone resources → one node each.
	resRows, err := s.db.Query(ctx, `
		SELECT id::text, name, provider, region, resource_type, status
		FROM provisioned_resources
		WHERE dismissed_at IS NULL
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list resources")
		return
	}
	defer resRows.Close()
	for resRows.Next() {
		var id, name, provider, region, resourceType, status string
		if err := resRows.Scan(&id, &name, &provider, &region, &resourceType, &status); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan resource")
			return
		}
		addNode(&topoNode{
			ID:           id,
			Kind:         "resource",
			RegionKey:    regionKeyFor(provider, region),
			Provider:     provider,
			Name:         name,
			ResourceType: resourceType,
			Status:       status,
			Apps:         []topoApp{},
		})
	}
	if err := resRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed reading resources")
		return
	}

	// 2) Pod-health rollup by namespace for the k8s "2/2" badge.
	podReady, podTotal, err := s.podCountsByNamespace(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read pod health")
		return
	}

	// 3) Apps → attach to their host node (cloud resource or synthetic local host).
	appHost := map[string]string{} // app id → host node id (for edge resolution)
	appRows, err := s.db.Query(ctx, `
		SELECT id::text, name, kind, COALESCE(framework,''), target_kind,
		       COALESCE(target_resource_id::text,''), status, COALESCE(namespace,''),
		       deployment_id::text, COALESCE(image_ref,''), COALESCE(port,0)
		FROM deployment_apps
		WHERE status <> 'destroyed'
		ORDER BY created_at ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list apps")
		return
	}
	defer appRows.Close()
	for appRows.Next() {
		var id, name, kind, framework, targetKind, targetResourceID, status, namespace, deploymentID, imageRef string
		var port int
		if err := appRows.Scan(&id, &name, &kind, &framework, &targetKind,
			&targetResourceID, &status, &namespace, &deploymentID, &imageRef, &port); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan app")
			return
		}

		hostID := hostNodeFor(targetKind, targetResourceID)
		if hostID == "" {
			continue // unknown target shape; skip rather than guess
		}
		// Cloud apps reference a resource node that must already exist; if it was
		// dismissed/destroyed the node is gone, so skip to avoid dangling chips.
		host, ok := nodes[hostID]
		if !ok {
			if hostID == localKindNodeID || hostID == localDockerNodeID {
				host = newLocalHostNode(hostID)
				addNode(host)
			} else {
				continue
			}
		}

		app := topoApp{
			ID:           id,
			Name:         name,
			Kind:         kind,
			Framework:    framework,
			TargetType:   appTargetType(targetKind),
			Status:       status,
			DeploymentID: deploymentID,
			ImageRef:     imageRef,
			Port:         port,
		}
		if targetKind == "local_k8s" && namespace != "" {
			app.PodsReady = podReady[namespace]
			app.PodsTotal = podTotal[namespace]
		}
		host.Apps = append(host.Apps, app)
		appHost[id] = hostID
	}
	if err := appRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed reading apps")
		return
	}

	// Local host node status is derived from the apps it carries.
	for _, id := range []string{localKindNodeID, localDockerNodeID} {
		if n, ok := nodes[id]; ok {
			n.Status = rollupAppStatus(n.Apps)
		}
	}

	// 4) Links → edges between host nodes. Only emit an edge when both endpoints
	// resolve to a node we actually have (guards against orphaned links).
	edges := []topoEdge{}
	linkRows, err := s.db.Query(ctx, `
		SELECT l.id::text, l.app_id::text,
		       COALESCE(l.depends_on_app_id::text,''),
		       COALESCE(l.depends_on_resource_id::text,''),
		       l.alias, COALESCE(l.port,0)
		FROM deployment_app_links l
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list links")
		return
	}
	defer linkRows.Close()
	for linkRows.Next() {
		var linkID, appID, dependsApp, dependsResource, alias string
		var port int
		if err := linkRows.Scan(&linkID, &appID, &dependsApp, &dependsResource, &alias, &port); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan link")
			return
		}
		fromNode, ok := appHost[appID]
		if !ok {
			continue
		}
		var toNode string
		switch {
		case dependsApp != "":
			toNode = appHost[dependsApp]
		case dependsResource != "":
			toNode = dependsResource // resource id is its own node id
		}
		if toNode == "" {
			continue
		}
		fromN, ok1 := nodes[fromNode]
		toN, ok2 := nodes[toNode]
		if !ok1 || !ok2 {
			continue
		}
		edges = append(edges, topoEdge{
			ID:          linkID,
			Kind:        "dependency",
			FromNode:    fromNode,
			ToNode:      toNode,
			FromRegion:  fromN.RegionKey,
			ToRegion:    toN.RegionKey,
			Alias:       alias,
			Port:        port,
			CrossRegion: fromN.RegionKey != toN.RegionKey,
		})
	}
	if err := linkRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed reading links")
		return
	}

	// 4b) Registry links: an ECS/ACI resource's bundled registry
	// (provisioned_resources.registry_resource_id, Phase 11) is a
	// resource-to-resource relationship, not an app link — draw it so the
	// ECS→ECR / ACI→ACR connection shows on the map.
	regRows, err := s.db.Query(ctx, `
		SELECT id::text, registry_resource_id::text
		FROM provisioned_resources
		WHERE dismissed_at IS NULL AND registry_resource_id IS NOT NULL
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list registry links")
		return
	}
	defer regRows.Close()
	for regRows.Next() {
		var serviceID, registryID string
		if err := regRows.Scan(&serviceID, &registryID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan registry link")
			return
		}
		fromN, ok1 := nodes[serviceID]
		toN, ok2 := nodes[registryID]
		if !ok1 || !ok2 {
			continue
		}
		edges = append(edges, topoEdge{
			ID:          "reg:" + serviceID,
			Kind:        "registry",
			FromNode:    serviceID,
			ToNode:      registryID,
			FromRegion:  fromN.RegionKey,
			ToRegion:    toN.RegionKey,
			Alias:       "registry",
			CrossRegion: fromN.RegionKey != toN.RegionKey,
		})
	}
	if err := regRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed reading registry links")
		return
	}

	// 5) Materialise regions from the nodes present, attach coordinates.
	localGeo := s.localGeo(ctx)
	regionCounts := map[string]int{}
	regionProvider := map[string]string{}
	regionRegion := map[string]string{}
	for _, n := range nodes {
		regionCounts[n.RegionKey]++
		regionProvider[n.RegionKey] = n.Provider
		// derive the bare region code from the key ("aws:eu-west-1" → "eu-west-1")
		if i := strings.IndexByte(n.RegionKey, ':'); i >= 0 {
			regionRegion[n.RegionKey] = n.RegionKey[i+1:]
		}
	}
	regions := make([]topoRegion, 0, len(regionCounts))
	for key, count := range regionCounts {
		reg := topoRegion{
			Key:       key,
			Provider:  regionProvider[key],
			Region:    regionRegion[key],
			NodeCount: count,
		}
		if key == localRegionKey {
			reg.Provider = "local"
			reg.Label = localGeo.Label
			reg.Lat, reg.Lon, reg.Located = localGeo.Lat, localGeo.Lon, true
		} else if g, ok := regionGeo[key]; ok {
			reg.Label = g.Label
			reg.Lat, reg.Lon, reg.Located = g.Lat, g.Lon, true
		} else {
			reg.Label = regionRegion[key] // unknown region: show its code, no pin
		}
		regions = append(regions, reg)
	}

	// Deterministic ordering for stable rendering and tests.
	sort.Slice(regions, func(i, j int) bool { return regions[i].Key < regions[j].Key })
	sort.Strings(order)
	orderedNodes := make([]topoNode, 0, len(order))
	for _, id := range order {
		orderedNodes = append(orderedNodes, *nodes[id])
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })

	writeJSON(w, http.StatusOK, topologyResponse{
		Regions: regions,
		Nodes:   orderedNodes,
		Edges:   edges,
	})
}

// hostNodeFor maps an app's target to the id of the node that hosts it: a
// cloud target's provisioned_resources id, or one of the two synthetic local
// host nodes. Returns "" for a shape we don't recognise.
func hostNodeFor(targetKind, targetResourceID string) string {
	switch targetKind {
	case "local_k8s":
		return localKindNodeID
	case "local_docker":
		return localDockerNodeID
	case "cloud_vm", "cloud_ecs", "cloud_aci":
		return targetResourceID // may be "" if unset; caller skips empties
	default:
		return ""
	}
}

func newLocalHostNode(id string) *topoNode {
	n := &topoNode{ID: id, RegionKey: localRegionKey, Provider: "local", Status: "pending", Apps: []topoApp{}}
	switch id {
	case localKindNodeID:
		n.Kind = "local_cluster"
		n.Name = "kind cluster"
		n.ResourceType = "kind"
	case localDockerNodeID:
		n.Kind = "docker_host"
		n.Name = "Docker host"
		n.ResourceType = "docker"
	}
	return n
}

// rollupAppStatus reduces a host node's apps to a single status: worst-case
// wins (failed > building/deploying > running > pending) so a red app turns
// its host node red.
func rollupAppStatus(apps []topoApp) string {
	if len(apps) == 0 {
		return "idle"
	}
	best := "pending"
	rank := map[string]int{"pending": 0, "running": 1, "deploying": 2, "building": 2, "failed": 3}
	for _, a := range apps {
		if rank[a.Status] > rank[best] {
			best = a.Status
		}
	}
	return best
}

// regionKeyFor builds the region grouping key. Local-provider resources fold
// into the single "local" region alongside local_k8s/local_docker apps.
func regionKeyFor(provider, region string) string {
	if provider == "local" || region == "" {
		return localRegionKey
	}
	return provider + ":" + region
}

// podCountsByNamespace rolls pod_health up into ready/total counts per
// namespace, used to render the "2/2" badge on k8s app chips.
func (s *Server) podCountsByNamespace(ctx context.Context) (ready, total map[string]int, err error) {
	ready, total = map[string]int{}, map[string]int{}
	rows, err := s.db.Query(ctx, `SELECT namespace, status FROM pod_health`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ns, status string
		if err := rows.Scan(&ns, &status); err != nil {
			return nil, nil, err
		}
		total[ns]++
		if strings.Contains(strings.ToLower(status), "run") {
			ready[ns]++
		}
	}
	return ready, total, rows.Err()
}

type geoPoint struct {
	Lat, Lon float64
	Label    string
}

// localGeo resolves the Local region pin. It reads the operator's override /
// previously auto-filled value from settings (topology_local_lat / _lon /
// _label) and otherwise falls back to a neutral default. When no coordinates
// are stored yet it kicks off a one-shot background GeoIP lookup of the
// server's public IP (topology_geoip.go); the manual override always wins and
// suppresses the lookup. Never blocks the request on a network call.
func (s *Server) localGeo(ctx context.Context) geoPoint {
	g := geoPoint{Lat: 51.5074, Lon: -0.1278, Label: "Local"} // neutral default (London)
	var haveLat, haveLon bool
	rows, err := s.db.Query(ctx,
		`SELECT key, value FROM settings WHERE key IN ('topology_local_lat','topology_local_lon','topology_local_label')`)
	if err != nil {
		return g
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return g
		}
		switch k {
		case "topology_local_lat":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				g.Lat, haveLat = f, true
			}
		case "topology_local_lon":
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				g.Lon, haveLon = f, true
			}
		case "topology_local_label":
			if v != "" {
				g.Label = v
			}
		}
	}
	if !haveLat || !haveLon {
		s.geoipOnce.Do(func() { go s.autofillLocalGeo() })
	}
	return g
}
