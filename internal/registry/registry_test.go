package registry

import (
	"strings"
	"testing"
)

func TestDefaultRepoTag(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		wantRepo string
		wantTag  string
	}{
		{"short name with tag", "postgres:16", "postgres", "16"},
		{"namespaced with tag", "bitnami/redis:7.2", "redis", "7.2"},
		{"registry host with tag", "ghcr.io/foo/bar:latest", "bar", "latest"},
		{"no tag defaults to latest", "postgres", "postgres", "latest"},
		{"digest reference defaults tag to latest", "postgres@sha256:" + strings.Repeat("0", 64), "postgres", "latest"},
		{"invalid reference", "INVALID REF", "", "latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, tag := DefaultRepoTag(tt.source)
			if repo != tt.wantRepo || tag != tt.wantTag {
				t.Errorf("DefaultRepoTag(%q) = (%q, %q), want (%q, %q)", tt.source, repo, tag, tt.wantRepo, tt.wantTag)
			}
		})
	}
}
