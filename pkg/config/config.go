package config

import "os"

type Config struct {
	DatabaseURL string
	Port        string
	JWTSecret   string
	MasterKey   string
}

func Load() *Config {
	return &Config{
		DatabaseURL: env("DATABASE_URL", "postgres://autodevops:autodevops@localhost:5432/autodevops?sslmode=disable"),
		Port:        env("PORT", "8080"),
		JWTSecret:   env("JWT_SECRET", ""),
		MasterKey:   env("MASTER_KEY", ""),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
