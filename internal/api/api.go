package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rexxzar/autodevops/internal/auth"
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
	publicIP      string
	ingressDomain string
	registryHost  string
}

func NewServer(db *pgxpool.Pool, a *auth.Auth, hub *sse.Hub, vault *secrets.Vault, orchestrator *deploy.Orchestrator, tfRunner *terraform.Runner, tfEngine *terraform.Engine, consoleManager *console.Manager, k8sCtrl *k8s.Controller, dockerMgr *docker.Manager, publicIP, ingressDomain, registryHost string) http.Handler {
	s := &Server{db: db, auth: a, hub: hub, vault: vault, deploy: orchestrator, tfRunner: tfRunner, tfEngine: tfEngine, console: consoleManager, k8sCtrl: k8sCtrl, dockerMgr: dockerMgr, publicIP: publicIP, ingressDomain: ingressDomain, registryHost: registryHost}

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
			r.Put("/deployments/{id}/apps/{appId}/secret-files", s.handleUpdateSecretFiles)
			r.Get("/deployments/{id}/apps/{appId}/k8s-manifests", s.handleGetK8sManifests)
			r.Post("/deployments/{id}/apps/{appId}/k8s-apply", s.handleApplyK8sManifest)
			r.Get("/deployments/{id}/apps/{appId}/workflow", s.handleGetAppWorkflow)
			r.Get("/deployments/{id}/apps/{appId}/env", s.handleGetAppEnv)
			r.Get("/deployments/{id}/apps/{appId}/repo/files", s.handleListRepoFiles)
			r.Get("/deployments/{id}/apps/{appId}/repo/files/*", s.handleGetRepoFile)
			r.Put("/deployments/{id}/apps/{appId}/repo/files/*", s.handlePutRepoFile)
			r.Post("/deployments/{id}/apps/{appId}/repo/commit", s.handleCommitRepo)
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
