package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/compliance"
	"github.com/Rexxzar/autodevops/internal/console"
	"github.com/Rexxzar/autodevops/internal/deploy"
	"github.com/Rexxzar/autodevops/internal/docker"
	"github.com/Rexxzar/autodevops/internal/k8s"
	"github.com/Rexxzar/autodevops/internal/secrets"
	"github.com/Rexxzar/autodevops/internal/sse"
	"github.com/Rexxzar/autodevops/internal/terraform"
)

type Server struct {
	db            *pgxpool.Pool
	auth          *auth.Auth
	hub           *sse.Hub
	vault         *secrets.Vault
	deploy        *deploy.Orchestrator
	tfRunner      *terraform.Runner
	tfEngine      *terraform.Engine
	console       *console.Manager
	k8sCtrl       *k8s.Controller
	dockerMgr     *docker.Manager
	compliance    *compliance.Scanner
	publicIP      string
	ingressDomain string
	registryHost  string

	// geoipOnce guards the one-shot background geolocation of the server's
	// public IP that seeds the Topology Map's Local pin (see topology_geoip.go).
	geoipOnce sync.Once
}

func NewServer(db *pgxpool.Pool, a *auth.Auth, hub *sse.Hub, vault *secrets.Vault, orchestrator *deploy.Orchestrator, tfRunner *terraform.Runner, tfEngine *terraform.Engine, consoleManager *console.Manager, k8sCtrl *k8s.Controller, dockerMgr *docker.Manager, complianceScanner *compliance.Scanner, publicIP, ingressDomain, registryHost string) http.Handler {
	s := &Server{db: db, auth: a, hub: hub, vault: vault, deploy: orchestrator, tfRunner: tfRunner, tfEngine: tfEngine, console: consoleManager, k8sCtrl: k8sCtrl, dockerMgr: dockerMgr, compliance: complianceScanner, publicIP: publicIP, ingressDomain: ingressDomain, registryHost: registryHost}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(requestLogger)
	r.Use(corsMiddleware)

	// Webhook endpoints — public, no /api/ prefix so distributed URLs never change.
	r.Post("/webhooks/github/{id}", s.handleGitHubWebhook)
	r.Post("/webhooks/gitlab/{id}", s.handleGitLabWebhook)

	// All API routes under /api/ so the Go binary can serve the React SPA at /*
	// without route conflicts. The Vite dev proxy rewrites /api/* → /* before
	// forwarding to the Go server in development, so the Go handlers stay prefix-free
	// internally — only the outer mounting path changes here.
	r.Route("/api", func(r chi.Router) {
		r.Get("/healthz", s.handleHealth)
		r.Post("/auth/login", s.handleLogin)
		r.Get("/dns/check", s.handleDNSCheck)

		r.Group(func(r chi.Router) {
			r.Use(a.Middleware)
			r.Get("/events", hub.Handler())
			r.Get("/topology", s.handleTopology)
			r.Get("/deployments", s.handleListDeployments)
			r.Post("/deployments", s.handleCreateDeployment)
			r.Post("/deployments/import/preview", s.handleImportComposePreview)
			r.Get("/deployments/{id}", s.handleGetDeployment)
			r.Delete("/deployments/{id}", s.handleDestroyDeployment)
			r.Post("/deployments/{id}/apps", s.handleAddApp)
			r.Delete("/deployments/{id}/apps/{appId}", s.handleDeleteApp)
			r.Post("/deployments/{id}/apps/{appId}/trigger", s.handleTriggerApp)
			r.Post("/deployments/{id}/apps/{appId}/links", s.handleCreateAppLink)
			r.Delete("/deployments/{id}/apps/{appId}/links/{linkId}", s.handleDeleteAppLink)
			r.Put("/deployments/{id}/apps/{appId}/domain", s.handleUpdateAppDomain)
			r.Put("/deployments/{id}/apps/{appId}/compliance", s.handleUpdateAppCompliance)
			r.Put("/deployments/{id}/apps/{appId}/secret-files", s.handleUpdateSecretFiles)
			r.Get("/deployments/{id}/apps/{appId}/k8s-manifests", s.handleGetK8sManifests)
			r.Post("/deployments/{id}/apps/{appId}/k8s-apply", s.handleApplyK8sManifest)
			r.Get("/deployments/{id}/apps/{appId}/workflow", s.handleGetAppWorkflow)
			r.Post("/deployments/{id}/apps/{appId}/cicd/install", s.handleInstallCICD)
			r.Get("/deployments/{id}/apps/{appId}/env", s.handleGetAppEnv)
			r.Get("/deployments/{id}/apps/{appId}/repo/files", s.handleListRepoFiles)
			r.Get("/deployments/{id}/apps/{appId}/repo/files/*", s.handleGetRepoFile)
			r.Put("/deployments/{id}/apps/{appId}/repo/files/*", s.handlePutRepoFile)
			r.Post("/deployments/{id}/apps/{appId}/repo/commit", s.handleCommitRepo)
			// Git Repo Store — first-class, reusable repositories.
			r.Get("/repos", s.handleListRepos)
			r.Post("/repos", s.handleCreateRepo)
			r.Get("/repos/{id}", s.handleGetRepo)
			r.Delete("/repos/{id}", s.handleDeleteRepo)
			r.Get("/repos/{id}/files", s.handleListStoreFiles)
			r.Get("/repos/{id}/files/*", s.handleGetStoreFile)
			r.Put("/repos/{id}/files/*", s.handlePutStoreFile)
			r.Post("/repos/{id}/commit", s.handleCommitStoreRepo)
			r.Post("/repos/{id}/refresh", s.handleRefreshStoreRepo)
			r.Post("/repos/{id}/scan", s.handleScanRepo)
			r.Get("/clusters", s.handleListClusters)
			r.Get("/clusters/pods", s.handleListPods)
			r.Get("/cloud/accounts", s.handleListCloudAccounts)
			r.Post("/cloud/accounts", s.handleCreateCloudAccount)
			r.Delete("/cloud/accounts/{id}", s.handleDeleteCloudAccount)
			r.Get("/cloud/accounts/{id}/networks", s.handleListCloudAccountNetworks)
			r.Get("/cloud/resources", s.handleListCloudResources)
			r.Get("/docker/networks", s.handleListDockerNetworks)
			r.Get("/registry/images", s.handleListRegistryImages)
			r.Delete("/registry/images", s.handleDeleteRegistryImage)
			r.Post("/registry/images/import", s.handleImportRegistryImage)
			r.Post("/cloud/resources/{id}/dismiss", s.handleDismissCloudResource)
			r.Get("/compliance/findings", s.handleListComplianceFindings)
			r.Post("/clusters", s.handleCreateCluster)
			r.Get("/terraform/runs", s.handleListTerraformRuns)
			// Terraform Store — workspace catalog, download/export, lifecycle.
			r.Get("/terraform/workspaces", s.handleListWorkspaces)
			r.Post("/terraform/import", s.handleImportTerraform)
			r.Get("/resources/{id}/terraform/download", s.handleDownloadResourceTerraform)
			r.Get("/resources/{id}/terraform/export", s.handleExportResourceTerraform)
			r.Get("/resources/{id}/findings", s.handleListResourceFindings)
			r.Delete("/resources/{id}/terraform/workspace", s.handleDeleteWorkspace)
			r.Get("/resources", s.handleListResources)
			r.Post("/resources", s.handleCreateResource)
			r.Post("/resources/preview", s.handlePreviewResource)
			r.Post("/resources/generate-ssh-key", s.handleGenerateSSHKey)
			r.Get("/resources/{id}", s.handleGetResource)
			r.Get("/resources/{id}/terraform", s.handleGetResourceTerraform)
			r.Post("/resources/{id}/terraform/plan", s.handlePlanResourceTerraform)
			r.Post("/resources/{id}/apply", s.handleApplyResource)
			r.Post("/resources/{id}/edit", s.handleEditResource)
			r.Put("/resources/{id}/registry", s.handleUpdateResourceRegistry)
			r.Post("/resources/{id}/destroy", s.handleDestroyResource)
			r.Post("/resources/{id}/dismiss", s.handleDismissResource)
			r.Post("/resources/{id}/console/sessions", s.handleOpenConsoleSession)
			r.Get("/resources/{id}/console/sessions/{sessionId}/stream", s.handleConsoleStream)
			r.Post("/resources/{id}/console/sessions/{sessionId}/input", s.handleConsoleInput)
			r.Post("/resources/{id}/console/sessions/{sessionId}/resize", s.handleConsoleResize)
			r.Delete("/resources/{id}/console/sessions/{sessionId}", s.handleCloseConsoleSession)
			r.Post("/resources/{id}/console/exec", s.handleConsoleExec)
			r.Get("/system", s.handleSystemInfo)
			r.Get("/logs", s.handleListLogs)
			r.Get("/logs/sources", s.handleLogSources)
			r.Get("/heal/episodes", s.handleListHealEpisodes)
			r.Get("/heal/metrics", s.handleHealMetrics)
			r.Get("/metrics/deploys", s.handleListDeployRuns)
			r.Get("/metrics/provision", s.handleListProvisionRuns)
			r.Get("/notifications", s.handleListNotifications)
			r.Get("/notifications/unread-count", s.handleUnreadCount)
			r.Post("/notifications/read", s.handleMarkNotificationsRead)
			r.Get("/secrets", s.handleListSecrets)
			r.Get("/secrets/status", s.handleVaultStatus)
			r.Post("/secrets/rotate", s.handleRotateDataKey)
			r.Post("/secrets/rotate-master", s.handleRotateMasterKey)
			r.Post("/secrets", s.handlePutSecret)
			r.Get("/secrets/{name}", s.handleGetSecret)
			r.Delete("/secrets/{name}", s.handleDeleteSecret)
			r.Get("/local/inventory", s.handleLocalInventory)
			r.Get("/local/inventory/{id}/logs", s.handleInventoryLogs)
			r.Post("/local/inventory/{id}/restart", s.handleInventoryRestart)
			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSetting)
		})
	})

	// React SPA — serve web/dist/ for all paths not matched above.
	// Static assets (JS/CSS/images) are served directly; any other path gets
	// index.html so React Router handles client-side navigation.
	r.Handle("/*", spaFileHandler("web/dist"))

	return r
}

// spaFileHandler serves the built React app from dir. Files that exist on disk
// (assets, fonts, icons) are served directly; everything else gets index.html.
func spaFileHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(start),
		)
	})
}
