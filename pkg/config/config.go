package config

import "os"

type Config struct {
	DatabaseURL    string
	Port           string
	JWTSecret      string
	MasterKey      string
	AdminEmail     string
	AdminPassword  string
	KubeconfigPath string
	ClusterName    string
	DataDir        string
}

func Load() *Config {
	return &Config{
		DatabaseURL:    env("DATABASE_URL", "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"),
		Port:           env("PORT", "8080"),
		JWTSecret:      env("JWT_SECRET", ""),
		MasterKey:      env("MASTER_KEY", ""),
		AdminEmail:     env("ADMIN_EMAIL", "admin@autodevops.local"),
		AdminPassword:  env("ADMIN_PASSWORD", "autodevops"),
		KubeconfigPath: env("KUBECONFIG", ""),
		ClusterName:    env("K8S_CLUSTER_NAME", "autodevops"),
		DataDir:        env("DATA_DIR", "./data"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
