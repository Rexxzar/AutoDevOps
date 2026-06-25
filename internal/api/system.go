package api

import "net/http"

// Version is the control-plane build version, overridable via -ldflags
// "-X github.com/Rexxzar/autodevops/internal/api.Version=<v>". Surfaced by
// /system (and the CLI's `autodev info`).
var Version = "dev"

// handleSystemInfo reports the control plane's version and which subsystems are
// active (derived from which dependencies were wired up at startup), plus the
// ingress/registry config — for `autodev info` and a future dashboard panel.
// GET /system
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version,
		"subsystems": map[string]bool{
			"kubernetes": s.k8sCtrl != nil,
			"docker":     s.dockerMgr != nil,
			"terraform":  s.tfRunner != nil,
			"compliance": s.compliance != nil,
			"console":    s.console != nil,
		},
		"ingress_domain": s.ingressDomain,
		"registry_host":  s.registryHost,
		"public_ip":      s.publicIP,
	})
}
