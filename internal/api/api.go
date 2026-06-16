package api

import (
	"log/slog"
	"net/http"
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

	r.Get("/healthz", s.handleHealth)
	r.Post("/auth/login", s.handleLogin)
	r.Get("/dns/check", s.handleDNSCheck)

	// GitHub can't present a JWT — security here is the per-app HMAC webhook
	// secret instead (see handleGitHubWebhook), so this route stays outside
	// the authenticated group. {id} is a deployment_apps.id, not a stack id.
	r.Post("/webhooks/github/{id}", s.handleGitHubWebhook)

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
		r.Get("/deployments/{id}/apps/{appId}/workflow", s.handleGetAppWorkflow)
		r.Get("/clusters", s.handleListClusters)
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

	return r
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
