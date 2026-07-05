package api

import (
	"strings"
	"testing"
)

func TestHostNodeFor(t *testing.T) {
	cases := []struct {
		targetKind, resourceID, want string
	}{
		{"local_k8s", "", localKindNodeID},
		{"local_docker", "", localDockerNodeID},
		{"cloud_vm", "res-1", "res-1"},
		{"cloud_ecs", "res-2", "res-2"},
		{"cloud_aci", "res-3", "res-3"},
		{"cloud_vm", "", ""},   // unset resource id → caller skips
		{"something", "x", ""}, // unknown shape
	}
	for _, c := range cases {
		if got := hostNodeFor(c.targetKind, c.resourceID); got != c.want {
			t.Errorf("hostNodeFor(%q,%q)=%q want %q", c.targetKind, c.resourceID, got, c.want)
		}
	}
}

func TestRegionKeyFor(t *testing.T) {
	cases := []struct {
		provider, region, want string
	}{
		{"aws", "eu-west-1", "aws:eu-west-1"},
		{"azure", "norwayeast", "azure:norwayeast"},
		{"local", "", localRegionKey},
		{"local", "whatever", localRegionKey},
		{"aws", "", localRegionKey}, // no region folds to local grouping
	}
	for _, c := range cases {
		if got := regionKeyFor(c.provider, c.region); got != c.want {
			t.Errorf("regionKeyFor(%q,%q)=%q want %q", c.provider, c.region, got, c.want)
		}
	}
}

func TestRollupAppStatus(t *testing.T) {
	cases := []struct {
		name string
		apps []topoApp
		want string
	}{
		{"empty", nil, "idle"},
		{"one running", []topoApp{{Status: "running"}}, "running"},
		{"failed wins", []topoApp{{Status: "running"}, {Status: "failed"}}, "failed"},
		{"building over running", []topoApp{{Status: "running"}, {Status: "building"}}, "building"},
		{"deploying over running", []topoApp{{Status: "running"}, {Status: "deploying"}}, "deploying"},
		{"all pending", []topoApp{{Status: "pending"}, {Status: "pending"}}, "pending"},
	}
	for _, c := range cases {
		if got := rollupAppStatus(c.apps); got != c.want {
			t.Errorf("%s: rollupAppStatus=%q want %q", c.name, got, c.want)
		}
	}
}

// regionGeo entries must line up with the "provider:region" key convention so
// handleTopology can resolve pins; a stray key would silently drop a pin.
func TestRegionGeoKeysWellFormed(t *testing.T) {
	for key, g := range regionGeo {
		if !strings.HasPrefix(key, "aws:") && !strings.HasPrefix(key, "azure:") {
			t.Errorf("regionGeo key %q must start with aws:/azure:", key)
		}
		if g.Lat < -90 || g.Lat > 90 || g.Lon < -180 || g.Lon > 180 {
			t.Errorf("regionGeo[%q] has out-of-range coords %v,%v", key, g.Lat, g.Lon)
		}
		if g.Label == "" {
			t.Errorf("regionGeo[%q] has empty label", key)
		}
	}
}
