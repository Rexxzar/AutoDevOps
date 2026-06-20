package deploy

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"

	"github.com/Rexxzar/autodevops/internal/sshutil"
)

// TLSProvisioner provisions TLS certificates and nginx vhost configs for
// local_docker deployments exposed via a custom subdomain.
//
// Flow (Provision):
//  1. DNS check — A record must resolve to p.publicIP
//  2. Detect nginx proxy (containerised or host nginx on port 80)
//  3. Write a temp nginx vhost that proxies /.well-known/acme-challenge/
//     to certbot standalone listening on a free high port — avoids needing
//     to stop nginx or have a webroot volume mount
//  4. Reload nginx, run certbot, reload again with the final TLS vhost
type TLSProvisioner struct {
	docker          *dockerclient.Client
	pool            *pgxpool.Pool
	publicIP        string
	certEmail       string
	kindIngressPort uint32 // host port the kind ingress-nginx controller serves plain HTTP on (ProvisionK8s upstream)
}

// NginxProxy describes the detected nginx reverse proxy on the host.
type NginxProxy struct {
	Containerized bool
	ContainerID   string // docker container ID (empty when not containerised)
	ConfDirHost   string // host-side path AutoDevOps can write .conf files to
	CertDirHost   string // host-side /etc/letsencrypt root (certbot writes here)
	CertDirInCtn  string // same path as seen inside the nginx container (for ssl_certificate)
	GatewayIP     string // IP nginx uses to reach the host (for ACME proxy_pass)
}

// NewTLSProvisioner builds a TLSProvisioner. publicIP is the server's public
// IPv4 (checked against DNS); certEmail is passed to certbot (may be empty —
// certbot will use --register-unsafely-without-email). kindIngressPort is the
// host port the kind ingress-nginx controller serves plain HTTP on — the
// upstream ProvisionK8s proxies a K8s app's domain to.
func NewTLSProvisioner(docker *dockerclient.Client, pool *pgxpool.Pool, publicIP, certEmail string, kindIngressPort uint32) *TLSProvisioner {
	return &TLSProvisioner{docker: docker, pool: pool, publicIP: publicIP, certEmail: certEmail, kindIngressPort: kindIngressPort}
}

// CheckDNS verifies that domain's A record resolves to publicIP.
// Returns a user-friendly error if it doesn't.
func CheckDNS(domain, publicIP string) error {
	addrs, err := net.LookupHost(domain)
	if err != nil {
		return fmt.Errorf("%s: DNS lookup failed: %w", domain, err)
	}
	for _, a := range addrs {
		if a == publicIP {
			return nil
		}
	}
	return fmt.Errorf("%s resolves to %v, not to this server (%s) — add an A record pointing to %s", domain, addrs, publicIP, publicIP)
}

// Provision runs the full TLS provisioning flow for a local_docker app — the
// nginx vhost proxies the domain to the app's deterministic container host port.
// publishStep is o.publishStep from the Orchestrator — lets TLS publish its
// own StepTLS events into the same pipeline SSE stream without coupling to
// the Orchestrator struct directly.
func (p *TLSProvisioner) Provision(ctx context.Context, appID, domain, stackName, appName string, publishStep func(appID, step, status string)) error {
	return p.provision(ctx, appID, domain, tlsAppHostPort(stackName, appName), publishStep)
}

// ProvisionK8s runs the same TLS flow for a local_k8s app, but the nginx vhost
// proxies the domain to the kind ingress-nginx controller's host HTTP port
// (p.kindIngressPort). The in-cluster ingress then Host-routes the request to
// the right Service — so multiple K8s apps share the one ingress upstream, and
// "proxy_set_header Host $host" in tlsFinalVhost is what disambiguates them.
func (p *TLSProvisioner) ProvisionK8s(ctx context.Context, appID, domain string, publishStep func(appID, step, status string)) error {
	return p.provision(ctx, appID, domain, p.kindIngressPort, publishStep)
}

// provision is the shared TLS flow for both targets — identical except for the
// nginx proxy_pass upstream port (a docker container host port for Provision, the
// kind ingress host port for ProvisionK8s).
func (p *TLSProvisioner) provision(ctx context.Context, appID, domain string, upstreamPort uint32, publishStep func(appID, step, status string)) error {
	pub := func(status string) { publishStep(appID, StepTLS, status) }

	pub(StepActive)
	if err := p.persistTLSStatus(ctx, appID, "provisioning", ""); err != nil {
		return err
	}

	// 1. DNS
	if err := CheckDNS(domain, p.publicIP); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "dns_failed",
			fmt.Sprintf("DNS not pointing here yet. Add an A record for %s → %s", domain, p.publicIP))
		return fmt.Errorf("tls: %w", err)
	}

	// 2. Detect nginx
	proxy, err := p.detectNginxProxy(ctx)
	if err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "No nginx found on port 80. Start an nginx container with a conf.d volume mount.")
		return fmt.Errorf("tls: detect nginx: %w", err)
	}
	slog.Info("tls: nginx detected", "containerized", proxy.Containerized, "conf_dir", proxy.ConfDirHost, "gateway", proxy.GatewayIP)

	// 3. Find a free port for certbot standalone
	acmePort, err := findFreePort()
	if err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: %w", err)
	}

	confName := domain + ".conf"
	confPath := filepath.Join(proxy.ConfDirHost, confName)

	// Write temp vhost and reload nginx so the ACME challenge proxy is live.
	tempConf := tlsTempVhost(domain, proxy.GatewayIP, acmePort)
	if err := os.WriteFile(confPath, []byte(tempConf), 0o644); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: write temp nginx vhost: %w", err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		_ = os.Remove(confPath)
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: nginx reload (temp): %w", err)
	}

	// 4. Run certbot standalone on acmePort — nginx proxies port-80 ACME
	//    traffic to us via the temp vhost above.
	certErr, certFixCmd := p.runCertbot(ctx, domain, acmePort, proxy)

	// Remove temp vhost regardless of certbot outcome.
	_ = os.Remove(confPath)
	_ = p.reloadNginx(ctx, proxy)

	if certErr != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "cert_failed", certFixCmd)
		return fmt.Errorf("tls: certbot: %w", certErr)
	}

	// 5. Write the final TLS vhost and reload.
	proxyTarget := proxyPassTarget(proxy, upstreamPort)
	finalConf := tlsFinalVhost(domain, proxyTarget, proxy.CertDirInCtn)
	if err := os.WriteFile(confPath, []byte(finalConf), 0o644); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: write final nginx vhost: %w", err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: nginx reload (final): %w", err)
	}

	_ = p.persistTLSStatus(ctx, appID, "active", "")
	pub(StepDone)
	return nil
}

// ProvisionCloud runs the TLS flow for a cloud-deployed app (ACI/ECS) whose
// upstream is a full URL (e.g. "http://myapp.norwayeast.azurecontainer.io:8080").
// The nginx vhost proxies the custom domain to this URL instead of a local port.
func (p *TLSProvisioner) ProvisionCloud(ctx context.Context, appID, domain, upstreamURL string, publishStep func(appID, step, status string)) error {
	return p.provisionWithTarget(ctx, appID, domain, upstreamURL, publishStep)
}

// ProvisionVMDirect provisions TLS directly on a cloud VM by writing an nginx
// vhost and running certbot over SSH. The domain must point to the VM's public
// IP (not the AutoDevOps server). Works for both EC2 (Amazon Linux) and Azure
// VM (Ubuntu) — nginx and certbot are pre-installed via user_data/custom_data.
func (p *TLSProvisioner) ProvisionVMDirect(ctx context.Context, appID, domain string, sshClient *ssh.Client, hostPort uint32, publishStep func(appID, step, status string)) error {
	pub := func(status string) { publishStep(appID, StepTLS, status) }
	pub(StepActive)
	if err := p.persistTLSStatus(ctx, appID, "provisioning", ""); err != nil {
		return err
	}

	run := func(cmd string) (sshutil.ExecResult, error) {
		return sshutil.ExecOnClient(sshClient, cmd, nil)
	}

	// 1. Ensure nginx is running
	if result, err := run("sudo nginx -v"); err != nil || result.ExitCode != 0 {
		slog.Warn("tls: nginx not found on VM, attempting install")
		if r, e := run("sudo apt-get update -qq && sudo apt-get install -y nginx 2>/dev/null || sudo dnf install -y nginx 2>/dev/null"); e != nil || r.ExitCode != 0 {
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "failed", "nginx is not installed on the VM and auto-install failed")
			return fmt.Errorf("tls: install nginx on VM: exit %d: %s", r.ExitCode, r.Stderr)
		}
		if _, e := run("sudo systemctl enable --now nginx"); e != nil {
			slog.Warn("tls: failed to start nginx on VM", "error", e)
		}
	}
	run("sudo rm -f /etc/nginx/sites-enabled/default")

	// 2. Ensure certbot is available
	if result, err := run("sudo certbot --version"); err != nil || result.ExitCode != 0 {
		slog.Warn("tls: certbot not found on VM, attempting install")
		if r, e := run("sudo apt-get install -y certbot python3-certbot-nginx 2>/dev/null || sudo dnf install -y certbot python3-certbot-nginx 2>/dev/null || sudo pip3 install certbot-nginx 2>/dev/null"); e != nil || r.ExitCode != 0 {
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "failed", "certbot is not installed on the VM and auto-install failed")
			return fmt.Errorf("tls: install certbot on VM: exit %d: %s", r.ExitCode, r.Stderr)
		}
	}

	confPath := "/etc/nginx/conf.d/" + domain + ".conf"
	certExists := false
	if result, err := run(fmt.Sprintf("sudo test -f /etc/letsencrypt/live/%s/fullchain.pem", domain)); err == nil && result.ExitCode == 0 {
		certExists = true
	}

	if certExists {
		// Cert already exists — write full SSL vhost directly, skip certbot
		vhost := fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name %s;

    ssl_certificate     /etc/letsencrypt/live/%s/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/%s/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;

    location / {
        proxy_pass http://localhost:%d;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}`, domain, domain, domain, domain, hostPort)

		writeResult, writeErr := sshutil.ExecOnClient(sshClient, "sudo tee "+confPath+" > /dev/null", strings.NewReader(vhost))
		if writeErr != nil || writeResult.ExitCode != 0 {
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "failed", "failed to write nginx vhost on VM")
			return fmt.Errorf("tls: write vhost on VM: exit %d: %s", writeResult.ExitCode, writeResult.Stderr)
		}
		if result, err := run("sudo nginx -s reload"); err != nil || result.ExitCode != 0 {
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "failed", "")
			return fmt.Errorf("tls: reload nginx on VM: exit %d: %s", result.ExitCode, result.Stderr)
		}
		slog.Info("tls: reused existing cert on VM", "domain", domain)
	} else {
		// No cert yet — write HTTP-only vhost, then run certbot to obtain cert
		vhost := fmt.Sprintf(`server {
    listen 80;
    server_name %s;

    location / {
        proxy_pass http://localhost:%d;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}`, domain, hostPort)

		writeResult, writeErr := sshutil.ExecOnClient(sshClient, "sudo tee "+confPath+" > /dev/null", strings.NewReader(vhost))
		if writeErr != nil || writeResult.ExitCode != 0 {
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "failed", "failed to write nginx vhost on VM")
			return fmt.Errorf("tls: write vhost on VM: exit %d: %s", writeResult.ExitCode, writeResult.Stderr)
		}
		if result, err := run("sudo nginx -s reload"); err != nil || result.ExitCode != 0 {
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "failed", "")
			return fmt.Errorf("tls: reload nginx on VM: exit %d: %s", result.ExitCode, result.Stderr)
		}

		emailFlag := "--register-unsafely-without-email"
		if p.certEmail != "" && !strings.HasSuffix(p.certEmail, ".local") {
			emailFlag = "--email " + p.certEmail
		}
		certbotCmd := fmt.Sprintf("sudo certbot --nginx -d %s --non-interactive --agree-tos %s --redirect", domain, emailFlag)
		slog.Info("tls: running certbot on VM", "domain", domain)
		if result, err := run(certbotCmd); err != nil || result.ExitCode != 0 {
			stderr := ""
			if result.Stderr != "" {
				stderr = result.Stderr
			}
			pub(StepFailed)
			_ = p.persistTLSStatus(ctx, appID, "cert_failed", "certbot failed on VM: "+stderr)
			return fmt.Errorf("tls: certbot on VM: exit %d: %s", result.ExitCode, stderr)
		}
	}

	_ = p.persistTLSStatus(ctx, appID, "active", "")
	pub(StepDone)
	slog.Info("tls: VM TLS active", "domain", domain)
	return nil
}

// DeprovisionVMDirect removes the nginx vhost and certbot cert from a cloud VM
// via SSH — called when an app with a domain on a cloud_vm target is deleted.
func (p *TLSProvisioner) DeprovisionVMDirect(sshClient *ssh.Client, domain string) error {
	run := func(cmd string) { sshutil.ExecOnClient(sshClient, cmd, nil) }
	run(fmt.Sprintf("sudo rm -f /etc/nginx/conf.d/%s.conf", domain))
	run("sudo nginx -s reload")
	run(fmt.Sprintf("sudo certbot delete --cert-name %s --non-interactive", domain))
	return nil
}

// provisionWithTarget is identical to provision() except it takes a full
// proxy_pass target URL instead of computing it from a local port — used by
// ProvisionCloud for ACI/ECS where the upstream is a public cloud FQDN.
func (p *TLSProvisioner) provisionWithTarget(ctx context.Context, appID, domain, proxyTarget string, publishStep func(appID, step, status string)) error {
	pub := func(status string) { publishStep(appID, StepTLS, status) }

	pub(StepActive)
	if err := p.persistTLSStatus(ctx, appID, "provisioning", ""); err != nil {
		return err
	}

	if err := CheckDNS(domain, p.publicIP); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "dns_failed",
			fmt.Sprintf("DNS not pointing here yet. Add an A record for %s → %s", domain, p.publicIP))
		return fmt.Errorf("tls: %w", err)
	}

	proxy, err := p.detectNginxProxy(ctx)
	if err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "No nginx found on port 80. Start an nginx container with a conf.d volume mount.")
		return fmt.Errorf("tls: detect nginx: %w", err)
	}
	slog.Info("tls: nginx detected (cloud)", "containerized", proxy.Containerized, "conf_dir", proxy.ConfDirHost, "upstream", proxyTarget)

	acmePort, err := findFreePort()
	if err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: %w", err)
	}

	confName := domain + ".conf"
	confPath := filepath.Join(proxy.ConfDirHost, confName)

	tempConf := tlsTempVhost(domain, proxy.GatewayIP, acmePort)
	if err := os.WriteFile(confPath, []byte(tempConf), 0o644); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: write temp nginx vhost: %w", err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		_ = os.Remove(confPath)
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: nginx reload (temp): %w", err)
	}

	certErr, certFixCmd := p.runCertbot(ctx, domain, acmePort, proxy)
	_ = os.Remove(confPath)
	_ = p.reloadNginx(ctx, proxy)

	if certErr != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "cert_failed", certFixCmd)
		return fmt.Errorf("tls: certbot: %w", certErr)
	}

	finalConf := tlsFinalVhost(domain, proxyTarget, proxy.CertDirInCtn)
	if err := os.WriteFile(confPath, []byte(finalConf), 0o644); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: write final nginx vhost: %w", err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed", "")
		return fmt.Errorf("tls: nginx reload (final): %w", err)
	}

	_ = p.persistTLSStatus(ctx, appID, "active", "")
	pub(StepDone)
	return nil
}

// detectNginxProxy finds what's bound on host port 80 and returns its config.
// Checks containerised nginx first (Docker SDK), then falls back to a host
// nginx installation (/etc/nginx/conf.d or /etc/nginx/sites-enabled).
func (p *TLSProvisioner) detectNginxProxy(ctx context.Context) (*NginxProxy, error) {
	containers, err := p.docker.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	for _, c := range containers {
		for _, port := range c.Ports {
			if port.PublicPort != 80 || port.Type != "tcp" {
				continue
			}
			// This container owns port 80. Inspect it for mounts and networks.
			inspect, inspErr := p.docker.ContainerInspect(ctx, c.ID)
			if inspErr != nil {
				continue
			}

			proxy := &NginxProxy{
				Containerized: true,
				ContainerID:   c.ID,
				CertDirHost:   "/etc/letsencrypt",
				CertDirInCtn:  "/etc/letsencrypt",
			}

			for _, m := range inspect.Mounts {
				dst := m.Destination
				if strings.Contains(dst, "nginx/conf.d") || strings.Contains(dst, "nginx/sites-enabled") {
					proxy.ConfDirHost = m.Source
				}
				if dst == "/etc/letsencrypt" || strings.HasPrefix(dst, "/etc/letsencrypt/") {
					proxy.CertDirHost = m.Source
					proxy.CertDirInCtn = dst
				}
			}

			if proxy.ConfDirHost == "" {
				name := ""
				if len(c.Names) > 0 {
					name = strings.TrimPrefix(c.Names[0], "/")
				}
				return nil, fmt.Errorf("nginx container %q has no conf.d/sites-enabled volume mount — add one so AutoDevOps can write vhost configs", name)
			}

			// Gateway IP is how nginx inside the container reaches the host.
			for _, net := range inspect.NetworkSettings.Networks {
				if net.Gateway != "" {
					proxy.GatewayIP = net.Gateway
					break
				}
			}
			if proxy.GatewayIP == "" {
				proxy.GatewayIP = "172.17.0.1" // sensible default
			}

			return proxy, nil
		}
	}

	// No containerised nginx — try host nginx.
	for _, dir := range []string{"/etc/nginx/conf.d", "/etc/nginx/sites-enabled"} {
		if _, statErr := os.Stat(dir); statErr == nil {
			return &NginxProxy{
				Containerized: false,
				ConfDirHost:   dir,
				CertDirHost:   "/etc/letsencrypt",
				CertDirInCtn:  "/etc/letsencrypt",
				GatewayIP:     "127.0.0.1",
			}, nil
		}
	}

	// Nothing found — auto-bootstrap an nginx container.
	return p.bootstrapNginx(ctx)
}

const (
	nginxContainerName = "autodevops-nginx"
	nginxImage         = "nginx:1-alpine"
	nginxConfDir       = "/etc/autodevops/nginx/conf.d"
	nginxCertDir       = "/etc/letsencrypt"
)

// bootstrapNginx creates and starts an nginx Docker container with the right
// volume mounts for AutoDevOps TLS provisioning — called automatically when
// detectNginxProxy finds no existing nginx on port 80.
func (p *TLSProvisioner) bootstrapNginx(ctx context.Context) (*NginxProxy, error) {
	slog.Info("tls: no nginx found, auto-bootstrapping nginx container")

	// Ensure host dirs exist.
	for _, dir := range []string{nginxConfDir, nginxCertDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	// Pull the image if not present.
	if _, _, err := p.docker.ImageInspectWithRaw(ctx, nginxImage); err != nil {
		slog.Info("tls: pulling nginx image", "image", nginxImage)
		reader, err := p.docker.ImagePull(ctx, "docker.io/library/"+nginxImage, image.PullOptions{})
		if err != nil {
			return nil, fmt.Errorf("pull nginx image: %w", err)
		}
		io.Copy(io.Discard, reader)
		reader.Close()
	}

	// Remove stale container with the same name (e.g. from a previous run).
	p.docker.ContainerRemove(ctx, nginxContainerName, container.RemoveOptions{Force: true})

	port80 := nat.Port("80/tcp")
	port443 := nat.Port("443/tcp")

	cfg := &container.Config{
		Image: nginxImage,
		ExposedPorts: nat.PortSet{
			port80:  struct{}{},
			port443: struct{}{},
		},
		Labels: map[string]string{
			"autodevops.io/managed-by": "autodevops",
			"autodevops.io/role":       "nginx-proxy",
		},
	}
	hostCfg := &container.HostConfig{
		PortBindings: nat.PortMap{
			port80:  []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "80"}},
			port443: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "443"}},
		},
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: nginxConfDir, Target: "/etc/nginx/conf.d", ReadOnly: false},
			{Type: mount.TypeBind, Source: nginxCertDir, Target: "/etc/letsencrypt", ReadOnly: false},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}

	resp, err := p.docker.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, nginxContainerName)
	if err != nil {
		return nil, fmt.Errorf("create nginx container: %w", err)
	}
	if err := p.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start nginx container: %w", err)
	}

	slog.Info("tls: nginx container started", "id", resp.ID[:12], "name", nginxContainerName)

	// Inspect to get the gateway IP.
	inspect, err := p.docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		return nil, fmt.Errorf("inspect nginx container: %w", err)
	}

	gatewayIP := "172.17.0.1"
	for _, net := range inspect.NetworkSettings.Networks {
		if net.Gateway != "" {
			gatewayIP = net.Gateway
			break
		}
	}

	return &NginxProxy{
		Containerized: true,
		ContainerID:   resp.ID,
		ConfDirHost:   nginxConfDir,
		CertDirHost:   nginxCertDir,
		CertDirInCtn:  "/etc/letsencrypt",
		GatewayIP:     gatewayIP,
	}, nil
}

// reloadNginx signals nginx to reload its configuration.
func (p *TLSProvisioner) reloadNginx(ctx context.Context, proxy *NginxProxy) error {
	if proxy.Containerized {
		created, err := p.docker.ContainerExecCreate(ctx, proxy.ContainerID, container.ExecOptions{
			Cmd:          []string{"nginx", "-s", "reload"},
			AttachStdout: true,
			AttachStderr: true,
		})
		if err != nil {
			return fmt.Errorf("exec create: %w", err)
		}
		resp, err := p.docker.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
		if err != nil {
			return fmt.Errorf("exec attach: %w", err)
		}
		resp.Close()
		ins, err := p.docker.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return fmt.Errorf("exec inspect: %w", err)
		}
		if ins.ExitCode != 0 {
			return fmt.Errorf("nginx -s reload exited %d", ins.ExitCode)
		}
		return nil
	}
	// Host nginx.
	out, err := exec.CommandContext(ctx, "nginx", "-s", "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx -s reload: %w\n%s", err, string(out))
	}
	return nil
}

// runCertbot invokes certbot standalone on acmePort. certbot's HTTP-01
// listener is on acmePort; the temp nginx vhost proxies /.well-known/ from
// port 80 → acmePort so Let's Encrypt can reach it.
//
// Returns (err, fixCmd): fixCmd is an actionable shell command to show in the
// dashboard whenever err != nil, or empty if there's no specific fix.
//
// Tries plain "certbot" first; if the process exits due to a permission error
// (certbot's default dirs are root-owned), retries with "sudo -n certbot".
// To enable the sudo path without a password prompt, add to /etc/sudoers:
//
//	<user> ALL=(ALL) NOPASSWD: /usr/bin/certbot
func (p *TLSProvisioner) runCertbot(ctx context.Context, domain string, acmePort int, proxy *NginxProxy) (error, string) {
	certbotPath, err := exec.LookPath("certbot")
	if err != nil {
		return fmt.Errorf("certbot not found on PATH — install it first (e.g. sudo apt install certbot)"),
			"sudo apt install -y certbot"
	}
	args := []string{
		"certonly", "--standalone",
		"--http-01-port", strconv.Itoa(acmePort),
		"-d", domain,
		"--non-interactive", "--agree-tos",
	}
	if p.certEmail != "" {
		args = append(args, "--email", p.certEmail)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}

	out, err := exec.CommandContext(ctx, certbotPath, args...).CombinedOutput()
	if err == nil {
		return nil, ""
	}
	outStr := string(out)

	// Permission error — the default certbot dirs (/var/log/letsencrypt etc.)
	// are root-owned. Retry with sudo -n (non-interactive sudo).
	if strings.Contains(outStr, "Permission denied") || strings.Contains(outStr, "Errno 13") {
		sudoArgs := append([]string{"-n", certbotPath}, args...)
		out2, err2 := exec.CommandContext(ctx, "sudo", sudoArgs...).CombinedOutput()
		if err2 == nil {
			return nil, ""
		}
		// sudo failed — most likely NOPASSWD not configured.
		fixCmd := fmt.Sprintf(
			"echo \"$(whoami) ALL=(ALL) NOPASSWD: %s\" | sudo tee /etc/sudoers.d/certbot-autodevops && sudo chmod 440 /etc/sudoers.d/certbot-autodevops",
			certbotPath,
		)
		return fmt.Errorf("certbot failed (permission denied, sudo also failed): %w\n%s", err2, strings.TrimSpace(string(out2))),
			fixCmd
	}

	// Challenge timeout / 504 — most likely the ACME challenge port is blocked
	// by a firewall. When nginx runs in Docker, it reaches the host via the
	// bridge gateway (proxy.GatewayIP / 172.18.x.1); ufw blocks that by default.
	if strings.Contains(outStr, "504") || strings.Contains(outStr, "Timeout") ||
		strings.Contains(outStr, "Connection refused") || strings.Contains(outStr, "Challenge did not pass") ||
		strings.Contains(outStr, "Unauthorized") {
		gatewayNet := "172.18.0.0/16"
		if proxy != nil && proxy.GatewayIP != "" {
			// derive /16 from the gateway so the rule is precise
			parts := strings.Split(proxy.GatewayIP, ".")
			if len(parts) == 4 {
				gatewayNet = parts[0] + "." + parts[1] + ".0.0/16"
			}
		}
		fixCmd := fmt.Sprintf(
			"sudo ufw allow from %s to any port %d:%d proto tcp",
			gatewayNet, 9200, 9299,
		)
		return fmt.Errorf("certbot ACME challenge failed (Let's Encrypt could not reach the server): %w", err),
			fixCmd
	}

	return fmt.Errorf("%w\ncertbot output:\n%s", err, strings.TrimSpace(outStr)), ""
}

// Deprovision removes the nginx vhost config for domain and reloads nginx.
// Called when an app with a TLS domain is deleted so the stale vhost doesn't
// keep nginx returning 502 for the domain.
func (p *TLSProvisioner) Deprovision(ctx context.Context, domain string) error {
	proxy, err := p.detectNginxProxy(ctx)
	if err != nil {
		return fmt.Errorf("tls: deprovision %s: detect nginx: %w", domain, err)
	}
	confPath := filepath.Join(proxy.ConfDirHost, domain+".conf")
	if err := os.Remove(confPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tls: deprovision %s: remove vhost: %w", domain, err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		return fmt.Errorf("tls: deprovision %s: reload nginx: %w", domain, err)
	}
	return nil
}

// persistTLSStatus writes deployment_apps.tls_status (and optionally tls_error) for appID.
// errMsg is the actionable fix command to show in the dashboard; empty clears the column.
func (p *TLSProvisioner) persistTLSStatus(ctx context.Context, appID, status, errMsg string) error {
	_, err := p.pool.Exec(ctx,
		"UPDATE deployment_apps SET tls_status = $1, tls_error = NULLIF($2, '') WHERE id = $3",
		status, errMsg, appID)
	return err
}

// findFreePort returns the first available TCP port in the 9200-9299 range.
func findFreePort() (int, error) {
	for port := 9200; port < 9300; port++ {
		l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err == nil {
			l.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free TCP port found in range 9200–9299")
}

// proxyPassTarget returns the nginx proxy_pass target for the deployed
// container. Containerised nginx reaches the host via its docker gateway IP;
// host nginx uses 127.0.0.1.
func proxyPassTarget(proxy *NginxProxy, hostPort uint32) string {
	ip := "127.0.0.1"
	if proxy.Containerized {
		ip = proxy.GatewayIP
	}
	return fmt.Sprintf("http://%s:%d", ip, hostPort)
}

// tlsSanitizeName mirrors internal/docker.sanitizeDockerName — lowercase +
// underscores→hyphens, so the FNV hash matches the actual container name.
func tlsSanitizeName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// tlsAppHostPort duplicates internal/docker.AppHostPort (package-boundary
// rule — same pattern as sanitizeSecretKey duplication in Phase 10).
// fnv32a hash of "<sanitized-stack>-<sanitized-app>", modulo 900, offset 8100.
func tlsAppHostPort(stackName, appName string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(tlsSanitizeName(stackName) + "-" + tlsSanitizeName(appName)))
	return 8100 + (h.Sum32() % 900)
}

// tlsTempVhost generates a minimal nginx server block that proxies ACME
// challenge requests to certbot standalone on acmePort (running on the host,
// reachable from the nginx container via gatewayIP).
func tlsTempVhost(domain, gatewayIP string, acmePort int) string {
	return fmt.Sprintf(`# AutoDevOps — temporary ACME challenge vhost for %s
server {
    listen 80;
    server_name %s;

    location /.well-known/acme-challenge/ {
        proxy_pass http://%s:%d;
    }
    location / {
        return 301 https://$host$request_uri;
    }
}
`, domain, domain, gatewayIP, acmePort)
}

// tlsFinalVhost generates the production nginx vhost with TLS termination and
// a reverse-proxy to the deployed Docker container's host port.
func tlsFinalVhost(domain, proxyTarget, certDirInCtn string) string {
	isCloudUpstream := strings.Contains(proxyTarget, ".azurecontainer.io") ||
		strings.Contains(proxyTarget, ".amazonaws.com") ||
		strings.Contains(proxyTarget, ".elb.amazonaws.com")

	var resolverBlock, proxyPassBlock string
	if isCloudUpstream {
		resolverBlock = "    resolver 8.8.8.8 1.1.1.1 valid=30s;\n    set $upstream " + proxyTarget + ";\n"
		proxyPassBlock = "        proxy_pass $upstream;\n"
	} else {
		proxyPassBlock = "        proxy_pass " + proxyTarget + ";\n"
	}

	return fmt.Sprintf(`# AutoDevOps — %s
server {
    listen 80;
    server_name %s;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name %s;

    ssl_certificate     %s/live/%s/fullchain.pem;
    ssl_certificate_key %s/live/%s/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;

%s    location / {
%s        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
`, domain, domain, domain, certDirInCtn, domain, certDirInCtn, domain, resolverBlock, proxyPassBlock)
}
