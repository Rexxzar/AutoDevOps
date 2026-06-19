package config

import (
	"os"
	"time"
)

type Config struct {
	DatabaseURL       string
	Port              string
	JWTSecret         string
	MasterKey         string
	AdminEmail        string
	AdminPassword     string
	KubeconfigPath    string
	ClusterName       string
	DataDir           string
	CloudPollInterval time.Duration
	// Paketo Buildpacks (T13)
	BuilderImage string // CNB builder image, e.g. paketobuildpacks/builder-jammy-full:latest
	RegistryPush string // host:port pack pushes to (host-accessible), e.g. localhost:5001
	RegistryPull string // host:port k8s manifests reference (in-cluster), e.g. registry.local:5000
	// KindIngressHTTPPort is the host port the kind cluster's ingress-nginx
	// controller listens on for plain HTTP. central nginx proxies a K8s app's
	// real domain to "http://<gateway>:<KindIngressHTTPPort>" (Host-routed by
	// the in-cluster ingress). Defaults to 8880 — matches setup-kind.sh's
	// fallback when host ports 80/443 are already taken by central nginx.
	KindIngressHTTPPort string
	// K8s manifests (T14)
	// base domain for generated Ingress hostnames: "<app>.<IngressDomain>".
	// The "apps." label is required, not cosmetic — sslip.io resolves an FQDN
	// by taking the leftmost dot-separated run of 4 numbers it finds, so an
	// app name ending in "-<digit>" (e.g. "multiapp-test-2") right before a
	// bare "127.0.0.1.sslip.io" gets misparsed as IP "2.127.0.0". A non-numeric
	// label between the app name and the IP avoids the false match.
	IngressDomain string
	// Compliance gate (T16)
	ComplianceThreshold string // minimum severity that blocks deploy: critical|high|medium|low|info
	// DNS / TLS
	PublicIP string // server's public IP shown to users when DNS doesn't point here yet
	// RegistryLifecycle is the registry address as seen from inside pack lifecycle
	// containers (e.g. "172.17.0.1:5001"). Auto-detected from Docker bridge gateway
	// when RegistryPush uses localhost; set REGISTRY_LIFECYCLE_ADDR to override.
	RegistryLifecycle string
}

func Load() *Config {
	return &Config{
		DatabaseURL:         env("DATABASE_URL", "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"),
		Port:                env("PORT", "8080"),
		JWTSecret:           env("JWT_SECRET", ""),
		MasterKey:           env("MASTER_KEY", ""),
		AdminEmail:          env("ADMIN_EMAIL", "admin@autodevops.local"),
		AdminPassword:       env("ADMIN_PASSWORD", "autodevops"),
		KubeconfigPath:      env("KUBECONFIG", ""),
		ClusterName:         env("K8S_CLUSTER_NAME", "autodevops"),
		DataDir:             env("DATA_DIR", "./data"),
		CloudPollInterval:   envDuration("CLOUD_POLL_INTERVAL", 60*time.Second),
		BuilderImage:        env("BUILDER_IMAGE", "paketobuildpacks/builder-jammy-full:latest"),
		RegistryPush:        env("REGISTRY_PUSH", "localhost:5001"),
		RegistryPull:        env("REGISTRY_PULL", "registry.local:5000"),
		IngressDomain:       env("INGRESS_DOMAIN", "apps.127.0.0.1.sslip.io"),
		KindIngressHTTPPort: env("KIND_INGRESS_HTTP_PORT", "8880"),
		ComplianceThreshold: env("COMPLIANCE_THRESHOLD", "high"),
		PublicIP:            env("PUBLIC_IP", ""),
		RegistryLifecycle:   env("REGISTRY_LIFECYCLE_ADDR", ""),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
