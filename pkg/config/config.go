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
	// K8s manifests (T14)
	IngressDomain string // base domain for generated Ingress hostnames, e.g. autodevops.local
	// Compliance gate (T16)
	ComplianceThreshold string // minimum severity that blocks deploy: critical|high|medium|low|info
	// DNS / TLS
	PublicIP string // server's public IP shown to users when DNS doesn't point here yet
}

func Load() *Config {
	return &Config{
		DatabaseURL:       env("DATABASE_URL", "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"),
		Port:              env("PORT", "8080"),
		JWTSecret:         env("JWT_SECRET", ""),
		MasterKey:         env("MASTER_KEY", ""),
		AdminEmail:        env("ADMIN_EMAIL", "admin@autodevops.local"),
		AdminPassword:     env("ADMIN_PASSWORD", "autodevops"),
		KubeconfigPath:    env("KUBECONFIG", ""),
		ClusterName:       env("K8S_CLUSTER_NAME", "autodevops"),
		DataDir:           env("DATA_DIR", "./data"),
		CloudPollInterval: envDuration("CLOUD_POLL_INTERVAL", 60*time.Second),
		BuilderImage:      env("BUILDER_IMAGE", "paketobuildpacks/builder-jammy-full:latest"),
		RegistryPush:      env("REGISTRY_PUSH", "localhost:5001"),
		RegistryPull:      env("REGISTRY_PULL", "registry.local:5000"),
		IngressDomain:       env("INGRESS_DOMAIN", "autodevops.local"),
		ComplianceThreshold: env("COMPLIANCE_THRESHOLD", "high"),
		PublicIP:            env("PUBLIC_IP", ""),
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
