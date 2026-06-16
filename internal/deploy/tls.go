package deploy

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
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
	docker    *dockerclient.Client
	pool      *pgxpool.Pool
	publicIP  string
	certEmail string
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
// certbot will use --register-unsafely-without-email).
func NewTLSProvisioner(docker *dockerclient.Client, pool *pgxpool.Pool, publicIP, certEmail string) *TLSProvisioner {
	return &TLSProvisioner{docker: docker, pool: pool, publicIP: publicIP, certEmail: certEmail}
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

// Provision runs the full TLS provisioning flow for a local_docker app.
// publishStep is o.publishStep from the Orchestrator — lets TLS publish its
// own StepTLS events into the same pipeline SSE stream without coupling to
// the Orchestrator struct directly.
func (p *TLSProvisioner) Provision(ctx context.Context, appID, domain, stackName, appName string, publishStep func(appID, step, status string)) error {
	pub := func(status string) { publishStep(appID, StepTLS, status) }

	pub(StepActive)
	if err := p.persistTLSStatus(ctx, appID, "provisioning"); err != nil {
		return err
	}

	// 1. DNS
	if err := CheckDNS(domain, p.publicIP); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "dns_failed")
		return fmt.Errorf("tls: %w", err)
	}

	// 2. Detect nginx
	proxy, err := p.detectNginxProxy(ctx)
	if err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed")
		return fmt.Errorf("tls: detect nginx: %w", err)
	}

	// 3. Find a free port for certbot standalone
	acmePort, err := findFreePort()
	if err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed")
		return fmt.Errorf("tls: %w", err)
	}

	confName := domain + ".conf"
	confPath := filepath.Join(proxy.ConfDirHost, confName)

	// Write temp vhost and reload nginx so the ACME challenge proxy is live.
	tempConf := tlsTempVhost(domain, proxy.GatewayIP, acmePort)
	if err := os.WriteFile(confPath, []byte(tempConf), 0o644); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed")
		return fmt.Errorf("tls: write temp nginx vhost: %w", err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		_ = os.Remove(confPath)
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed")
		return fmt.Errorf("tls: nginx reload (temp): %w", err)
	}

	// 4. Run certbot standalone on acmePort — nginx proxies port-80 ACME
	//    traffic to us via the temp vhost above.
	certErr := p.runCertbot(ctx, domain, acmePort)

	// Remove temp vhost regardless of certbot outcome.
	_ = os.Remove(confPath)
	_ = p.reloadNginx(ctx, proxy)

	if certErr != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "cert_failed")
		return fmt.Errorf("tls: certbot: %w", certErr)
	}

	// 5. Write the final TLS vhost and reload.
	hostPort := tlsAppHostPort(stackName, appName)
	proxyTarget := proxyPassTarget(proxy, hostPort)
	finalConf := tlsFinalVhost(domain, proxyTarget, proxy.CertDirInCtn)
	if err := os.WriteFile(confPath, []byte(finalConf), 0o644); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed")
		return fmt.Errorf("tls: write final nginx vhost: %w", err)
	}
	if err := p.reloadNginx(ctx, proxy); err != nil {
		pub(StepFailed)
		_ = p.persistTLSStatus(ctx, appID, "failed")
		return fmt.Errorf("tls: nginx reload (final): %w", err)
	}

	_ = p.persistTLSStatus(ctx, appID, "active")
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

	return nil, fmt.Errorf("no nginx found on port 80 — either a Docker container or a host nginx installation is required")
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
// Tries plain "certbot" first; if the process exits due to a permission error
// (certbot's default dirs are root-owned), retries with "sudo -n certbot".
// To enable the sudo path without a password prompt, add to /etc/sudoers:
//
//	<user> ALL=(ALL) NOPASSWD: /usr/bin/certbot
func (p *TLSProvisioner) runCertbot(ctx context.Context, domain string, acmePort int) error {
	certbotPath, err := exec.LookPath("certbot")
	if err != nil {
		return fmt.Errorf("certbot not found on PATH — install it first (e.g. sudo apt install certbot)")
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
		return nil
	}
	// Permission error — the default certbot dirs (/var/log/letsencrypt etc.)
	// are root-owned. Retry with sudo -n (non-interactive sudo).
	if strings.Contains(string(out), "Permission denied") || strings.Contains(string(out), "Errno 13") {
		sudoArgs := append([]string{"-n", certbotPath}, args...)
		out2, err2 := exec.CommandContext(ctx, "sudo", sudoArgs...).CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("certbot failed (even with sudo -n).\n"+
				"Add this to /etc/sudoers to fix: $(whoami) ALL=(ALL) NOPASSWD: %s\n"+
				"sudo output:\n%s", certbotPath, strings.TrimSpace(string(out2)))
		}
		return nil
	}
	return fmt.Errorf("%w\ncertbot output:\n%s", err, strings.TrimSpace(string(out)))
}

// persistTLSStatus writes deployment_apps.tls_status for appID.
func (p *TLSProvisioner) persistTLSStatus(ctx context.Context, appID, status string) error {
	_, err := p.pool.Exec(ctx, "UPDATE deployment_apps SET tls_status = $1 WHERE id = $2", status, appID)
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

// tlsAppHostPort duplicates internal/docker.AppHostPort (package-boundary
// rule — same pattern as sanitizeSecretKey duplication in Phase 10).
// fnv32a hash of "<stack>-<app>", modulo 900, offset 8100.
func tlsAppHostPort(stackName, appName string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(stackName + "-" + appName))
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

    location / {
        proxy_pass %s;
        proxy_http_version 1.1;
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
`, domain, domain, domain, certDirInCtn, domain, certDirInCtn, domain, proxyTarget)
}
