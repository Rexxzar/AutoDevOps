package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/Rexxzar/autodevops/internal/auth"
	"github.com/Rexxzar/autodevops/internal/cicd"
	"github.com/Rexxzar/autodevops/internal/deploy"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDNSCheck(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "domain query param required")
		return
	}

	// Resolve the domain
	addrs, err := net.LookupHost(domain)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"domain":    domain,
			"ok":        false,
			"server_ip": s.serverIP(r),
			"error":     "domain does not resolve — check the name is correct",
		})
		return
	}

	serverIP := s.serverIP(r)
	ok := false
	for _, a := range addrs {
		if a == serverIP {
			ok = true
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":       domain,
		"resolved_ips": addrs,
		"server_ip":    serverIP,
		"ok":           ok,
	})
}

// serverIP returns the public IP to tell users to point their DNS at.
// Priority: PUBLIC_IP env var → X-Forwarded-For → remote addr.
func (s *Server) serverIP(r *http.Request) string {
	if s.publicIP != "" {
		return s.publicIP
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password required")
		return
	}

	var userID, passwordHash string
	err := s.db.QueryRow(r.Context(),
		"SELECT id::text, password_hash FROM users WHERE email = $1", req.Email,
	).Scan(&userID, &passwordHash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !auth.VerifyPassword(req.Password, passwordHash) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := s.auth.Issue(userID, req.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, name, repo_url, branch,
		       COALESCE(framework,''), target_type, status,
		       COALESCE(image_ref,''), created_at, updated_at
		FROM deployments ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	defer rows.Close()

	type row struct {
		ID         string    `json:"id"`
		Name       string    `json:"name"`
		RepoURL    string    `json:"repo_url"`
		Branch     string    `json:"branch"`
		Framework  string    `json:"framework,omitempty"`
		TargetType string    `json:"target_type"`
		Status     string    `json:"status"`
		ImageRef   string    `json:"image_ref,omitempty"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	}
	var results []row
	for rows.Next() {
		var d row
		if err := rows.Scan(&d.ID, &d.Name, &d.RepoURL, &d.Branch, &d.Framework,
			&d.TargetType, &d.Status, &d.ImageRef, &d.CreatedAt, &d.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan deployment")
			return
		}
		results = append(results, d)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleCreateDeployment registers a deployment for the Git-to-deploy
// orchestrator (T12). It generates a per-deployment webhook secret and
// (optionally) stores the user's chosen git auth — both encrypted in the
// vault, with deployments.webhook_secret_id/git_credentials_secret_id
// resolved to their UUIDs in the same INSERT — mirroring
// handleCreateCloudAccount's "encrypt before it touches the database" shape.
// The response carries the webhook URL + plaintext secret exactly once, for
// the user to paste into GitHub's repo → Settings → Webhooks.
func (s *Server) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		RepoURL    string `json:"repo_url"`
		Branch     string `json:"branch"`
		TargetType string `json:"target_type"`
		GitAuth    *struct {
			Type   string `json:"type"`
			Token  string `json:"token,omitempty"`
			SSHKey string `json:"ssh_key,omitempty"`
		} `json:"git_auth,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.RepoURL == "" {
		writeError(w, http.StatusBadRequest, "name and repo_url required")
		return
	}
	if req.TargetType != "k8s" && req.TargetType != "container" && req.TargetType != "vm" {
		writeError(w, http.StatusBadRequest, `target_type must be "k8s", "container", or "vm"`)
		return
	}
	if req.GitAuth != nil && req.GitAuth.Type != deploy.GitAuthToken && req.GitAuth.Type != deploy.GitAuthSSHKey {
		writeError(w, http.StatusBadRequest, `git_auth.type must be "token" or "ssh_key"`)
		return
	}

	branch := req.Branch
	if branch == "" {
		branch = "main"
	}

	ctx := r.Context()

	webhookSecretRaw := make([]byte, 32)
	if _, err := rand.Read(webhookSecretRaw); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate webhook secret")
		return
	}
	webhookSecret := hex.EncodeToString(webhookSecretRaw)

	webhookSecretName := "webhook:" + req.Name
	if err := s.vault.Put(ctx, webhookSecretName, []byte(webhookSecret)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store webhook secret")
		return
	}

	var gitCredsSecretName *string
	if req.GitAuth != nil {
		raw, err := json.Marshal(map[string]string{
			"type":    req.GitAuth.Type,
			"token":   req.GitAuth.Token,
			"ssh_key": req.GitAuth.SSHKey,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode git credentials")
			return
		}
		name := "git-credentials:" + req.Name
		if err := s.vault.Put(ctx, name, raw); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store git credentials")
			return
		}
		gitCredsSecretName = &name
	}

	var deploymentID string
	err := s.db.QueryRow(ctx, `
		INSERT INTO deployments (name, repo_url, branch, target_type, webhook_secret_id, git_credentials_secret_id)
		VALUES (
			$1, $2, $3, $4,
			(SELECT id FROM secrets WHERE name = $5),
			(SELECT id FROM secrets WHERE name = $6)
		)
		RETURNING id::text
	`, req.Name, req.RepoURL, branch, req.TargetType, webhookSecretName, gitCredsSecretName).Scan(&deploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create deployment")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":             deploymentID,
		"webhook_url":    "/webhooks/github/" + deploymentID,
		"webhook_secret": webhookSecret,
	})
}

// handleGitHubWebhook receives GitHub push deliveries for a deployment.
// Outside the JWT-authenticated group — GitHub can't present a bearer token,
// so the per-deployment HMAC secret (X-Hub-Signature-256) is the auth
// boundary instead, exactly GitHub's documented webhook security model.
// Cloning happens asynchronously: GitHub times out webhook responses after
// ~10s, so this handler verifies, decides, and returns 202 immediately,
// leaving deploy.Orchestrator.ProcessPush to run in the background.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	deploymentID := chi.URLParam(r, "id")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var branch, webhookSecretID string
	err = s.db.QueryRow(r.Context(), `
		SELECT branch, webhook_secret_id::text FROM deployments WHERE id = $1
	`, deploymentID).Scan(&branch, &webhookSecretID)
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	secret, err := s.vault.GetByID(r.Context(), webhookSecretID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify signature")
		return
	}
	if !deploy.VerifyWebhookSignature(body, r.Header.Get("X-Hub-Signature-256"), secret) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
	case "push":
		var payload struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "invalid push payload")
			return
		}
		if payload.Ref != "refs/heads/"+branch {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "branch mismatch"})
			return
		}

		go func() {
			if err := s.deploy.ProcessPush(context.Background(), deploymentID); err != nil {
				slog.Error("deploy: process push", "deployment_id", deploymentID, "error", err)
			}
		}()

		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "unsupported event"})
	}
}

func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, name, endpoint, status, created_at
		FROM k8s_clusters ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list clusters")
		return
	}
	defer rows.Close()

	type row struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Endpoint  string    `json:"endpoint"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var c row
		if err := rows.Scan(&c.ID, &c.Name, &c.Endpoint, &c.Status, &c.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan cluster")
			return
		}
		results = append(results, c)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleListCloudAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, provider, name, created_at
		FROM cloud_accounts ORDER BY created_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cloud accounts")
		return
	}
	defer rows.Close()

	type row struct {
		ID        string    `json:"id"`
		Provider  string    `json:"provider"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var a row
		if err := rows.Scan(&a.ID, &a.Provider, &a.Name, &a.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan cloud account")
			return
		}
		results = append(results, a)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

// handleCreateCloudAccount registers a cloud account for the T11 poller to
// query. Credentials are stored encrypted in the vault under a derived secret
// name, and cloud_accounts.credentials_secret_id is resolved to that secret's
// UUID in the same INSERT — mirroring handlePutSecret's "encrypt before it
// touches the database" rule.
func (s *Server) handleCreateCloudAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider    string          `json:"provider"`
		Name        string          `json:"name"`
		Credentials json.RawMessage `json:"credentials"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Provider != "aws" && req.Provider != "azure" {
		writeError(w, http.StatusBadRequest, `provider must be "aws" or "azure"`)
		return
	}
	if req.Name == "" || len(req.Credentials) == 0 {
		writeError(w, http.StatusBadRequest, "name and credentials required")
		return
	}

	secretName := "cloud-account:" + req.Name
	if err := s.vault.Put(r.Context(), secretName, req.Credentials); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store credentials")
		return
	}

	var accountID string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO cloud_accounts (provider, name, credentials_secret_id)
		SELECT $1, $2, id FROM secrets WHERE name = $3
		RETURNING id::text
	`, req.Provider, req.Name, secretName).Scan(&accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create cloud account")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": accountID, "provider": req.Provider, "name": req.Name})
}

func (s *Server) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Value == "" {
		writeError(w, http.StatusBadRequest, "name and value required")
		return
	}
	if err := s.vault.Put(r.Context(), req.Name, []byte(req.Value)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store secret")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": req.Name})
}

func (s *Server) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	value, err := s.vault.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "value": string(value)})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.vault.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListComplianceFindings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT cf.id::text, cf.deployment_id::text, d.name,
		       cf.scanner, cf.severity, cf.rule_id, cf.message,
		       COALESCE(cf.file_path, ''), COALESCE(cf.line_number, 0),
		       cf.created_at
		FROM compliance_findings cf
		JOIN deployments d ON d.id = cf.deployment_id
		ORDER BY cf.created_at DESC
		LIMIT 500
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list findings")
		return
	}
	defer rows.Close()

	type row struct {
		ID             string    `json:"id"`
		DeploymentID   string    `json:"deployment_id"`
		DeploymentName string    `json:"deployment_name"`
		Scanner        string    `json:"scanner"`
		Severity       string    `json:"severity"`
		RuleID         string    `json:"rule_id"`
		Message        string    `json:"message"`
		FilePath       string    `json:"file_path,omitempty"`
		LineNumber     int       `json:"line_number,omitempty"`
		CreatedAt      time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var f row
		if err := rows.Scan(&f.ID, &f.DeploymentID, &f.DeploymentName,
			&f.Scanner, &f.Severity, &f.RuleID, &f.Message,
			&f.FilePath, &f.LineNumber, &f.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan finding")
			return
		}
		results = append(results, f)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleListCloudResources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT cr.id::text, ca.provider, ca.name,
		       cr.resource_type, cr.resource_id, cr.region,
		       COALESCE(cr.name, ''), cr.state::text, cr.polled_at
		FROM cloud_resources cr
		JOIN cloud_accounts ca ON ca.id = cr.cloud_account_id
		ORDER BY cr.polled_at DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cloud resources")
		return
	}
	defer rows.Close()

	type row struct {
		ID           string    `json:"id"`
		Provider     string    `json:"provider"`
		AccountName  string    `json:"account_name"`
		ResourceType string    `json:"resource_type"`
		ResourceID   string    `json:"resource_id"`
		Region       string    `json:"region"`
		Name         string    `json:"name,omitempty"`
		State        string    `json:"state"`
		PolledAt     time.Time `json:"polled_at"`
	}
	var results []row
	for rows.Next() {
		var cr row
		if err := rows.Scan(&cr.ID, &cr.Provider, &cr.AccountName,
			&cr.ResourceType, &cr.ResourceID, &cr.Region,
			&cr.Name, &cr.State, &cr.PolledAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan resource")
			return
		}
		results = append(results, cr)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleCreateCluster(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "name and endpoint required")
		return
	}

	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO k8s_clusters (name, endpoint, status)
		VALUES ($1, $2, 'unknown')
		ON CONFLICT (name) DO UPDATE SET endpoint = EXCLUDED.endpoint
		RETURNING id::text
	`, req.Name, req.Endpoint).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create cluster")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": req.Name, "endpoint": req.Endpoint})
}

func (s *Server) handleListTerraformRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, COALESCE(deployment_id::text,''), COALESCE(cloud_account_id::text,''),
		       template, action, status, COALESCE(plan_output,''), created_at
		FROM terraform_runs ORDER BY created_at DESC LIMIT 100
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list terraform runs")
		return
	}
	defer rows.Close()
	type row struct {
		ID             string    `json:"id"`
		DeploymentID   string    `json:"deployment_id,omitempty"`
		CloudAccountID string    `json:"cloud_account_id,omitempty"`
		Template       string    `json:"template"`
		Action         string    `json:"action"`
		Status         string    `json:"status"`
		PlanOutput     string    `json:"plan_output,omitempty"`
		CreatedAt      time.Time `json:"created_at"`
	}
	var results []row
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.ID, &rr.DeploymentID, &rr.CloudAccountID,
			&rr.Template, &rr.Action, &rr.Status, &rr.PlanOutput, &rr.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan run")
			return
		}
		results = append(results, rr)
	}
	if results == nil {
		results = []row{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleCreateTerraformRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string            `json:"name"`
		Provider       string            `json:"provider"`
		CloudAccountID string            `json:"cloud_account_id"`
		Region         string            `json:"region"`
		ResourceType   string            `json:"resource_type"`
		ResourceOpts   map[string]string `json:"resource_opts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Provider == "" || req.CloudAccountID == "" || req.Region == "" || req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "name, provider, cloud_account_id, region, resource_type required")
		return
	}
	if s.tfRunner == nil {
		writeError(w, http.StatusServiceUnavailable, "terraform binary unavailable — check server logs")
		return
	}

	config, err := buildTerraformConfig(req.Provider, req.ResourceType, req.Name, req.Region, req.ResourceOpts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Insert placeholder deployment so the run has a deployment_id foreign key.
	var deploymentID string
	_ = s.db.QueryRow(r.Context(), `
		INSERT INTO deployments (name, repo_url, branch, target_type, status)
		VALUES ($1, 'terraform:'||$1, 'main', 'cloud', 'pending')
		ON CONFLICT (name) DO UPDATE SET updated_at = NOW()
		RETURNING id::text
	`, req.Name).Scan(&deploymentID)

	go func() {
		ctx := context.Background()
		if _, err := s.tfRunner.Plan(ctx, deploymentID, req.CloudAccountID, req.Provider+"-stack", config); err != nil {
			slog.Error("terraform: plan failed", "name", req.Name, "error", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":      "plan_initiated",
		"name":        req.Name,
		"provider":    req.Provider,
		"description": "Terraform plan running — watch the SSE stream for terraform.run events",
	})
}

func strOpt(opts map[string]string, key, def string) string {
	if v, ok := opts[key]; ok && v != "" {
		return v
	}
	return def
}

func buildTerraformConfig(provider, resourceType, name, region string, opts map[string]string) ([]byte, error) {
	if opts == nil {
		opts = map[string]string{}
	}
	switch provider {
	case "aws":
		params := map[string]interface{}{
			"Name":    name,
			"Region":  region,
			"VPCCIDR": "10.0.0.0/16",
		}
		switch resourceType {
		case "ecs":
			params["ECS"] = map[string]interface{}{
				"Image":         strOpt(opts, "image", "nginx:latest"),
				"CPU":           strOpt(opts, "cpu", "256"),
				"Memory":        strOpt(opts, "memory", "512"),
				"ContainerPort": strOpt(opts, "port", "80"),
				"DesiredCount":  strOpt(opts, "desired_count", "1"),
			}
		case "vm":
			ami := strOpt(opts, "ami", "ami-0c101f26f147fa7fd")
			ec2 := map[string]interface{}{
				"InstanceType": strOpt(opts, "instance_type", "t3.micro"),
				"AMI":          ami,
			}
			if kn := strOpt(opts, "key_name", ""); kn != "" {
				ec2["KeyName"] = kn
			}
			params["EC2"] = ec2
		case "rds":
			params["RDS"] = map[string]interface{}{
				"Engine":        strOpt(opts, "engine", "postgres"),
				"InstanceClass": strOpt(opts, "instance_class", "db.t3.micro"),
				"DBName":        strOpt(opts, "db_name", name),
				"Username":      strOpt(opts, "username", "admin"),
			}
		}
		raw, _ := json.Marshal(params)
		return raw, nil
	case "azure":
		params := map[string]interface{}{
			"Name":       name,
			"Location":   region,
			"VNetCIDR":   "10.0.0.0/16",
			"SubnetCIDR": "10.0.1.0/24",
		}
		switch resourceType {
		case "aci":
			params["ACI"] = map[string]interface{}{
				"Image":    strOpt(opts, "image", "nginx:latest"),
				"CPU":      strOpt(opts, "cpu", "1"),
				"MemoryGB": strOpt(opts, "memory_gb", "1.5"),
				"Port":     strOpt(opts, "port", "80"),
				"OSType":   strOpt(opts, "os_type", "Linux"),
			}
		case "vm":
			params["VM"] = map[string]interface{}{
				"Size":          strOpt(opts, "size", "Standard_B1s"),
				"AdminUsername": strOpt(opts, "admin_username", "azureuser"),
				"ImageSKU":      strOpt(opts, "image_sku", "Ubuntu2204"),
			}
		case "db":
			params["DB"] = map[string]interface{}{
				"Engine":     strOpt(opts, "engine", "postgres"),
				"SKU":        strOpt(opts, "sku", "B_Gen5_1"),
				"DBName":     strOpt(opts, "db_name", name),
				"AdminLogin": strOpt(opts, "admin_login", "adminuser"),
			}
		}
		raw, _ := json.Marshal(params)
		return raw, nil
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

func (s *Server) handleDestroyDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag, err := s.db.Exec(r.Context(),
		`UPDATE deployments SET status = 'destroyed', updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to destroy deployment")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTriggerDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var exists bool
	if err := s.db.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM deployments WHERE id = $1)`, id,
	).Scan(&exists); err != nil || !exists {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	go func() {
		if err := s.deploy.ProcessPush(context.Background(), id); err != nil {
			slog.Error("deploy: manual trigger", "deployment_id", id, "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "deployment_id": id})
}

// handleGetDeploymentWorkflow generates and returns a GitHub Actions workflow
// YAML for the given deployment. The file is ready to commit verbatim to
// .github/workflows/autodevops.yml — credentials are never embedded, only
// referenced as repository secrets (AUTODEVOPS_WEBHOOK_SECRET, AUTODEVOPS_URL).
func (s *Server) handleGetDeploymentWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var params cicd.WorkflowParams
	params.DeploymentID = id
	err := s.db.QueryRow(r.Context(), `
		SELECT name, branch, COALESCE(framework, 'unknown')
		FROM deployments WHERE id = $1
	`, id).Scan(&params.Name, &params.Branch, &params.Framework)
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}

	yaml := cicd.Workflow(params)
	w.Header().Set("Content-Type", "text/x-yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="autodevops-`+params.Name+`.yml"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(yaml))
}
