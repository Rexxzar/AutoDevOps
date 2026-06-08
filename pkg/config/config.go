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
