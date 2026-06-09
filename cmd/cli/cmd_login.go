package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func loginCmd() *cobra.Command {
	var serverURL, email, password string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the AutoDevOps control plane",
		Example: `  autodev login --email admin@autodevops.local --password autodevops
  autodev login --server http://localhost:8080 --email admin@autodevops.local --password autodevops`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if serverURL != "" {
				cfg.ServerURL = serverURL
			}

			c := newClient(cfg)
			var resp struct {
				Token string `json:"token"`
			}
			if err := c.post(context.Background(), "/auth/login", map[string]string{
				"email":    email,
				"password": password,
			}, &resp); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			cfg.Token = resp.Token
			if err := saveConfig(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			success(fmt.Sprintf("Logged in to %s", cfg.ServerURL))
			fmt.Printf("  Config saved to %s\n", configPath())
			return nil
		},
	}

	cmd.Flags().StringVar(&serverURL, "server", "", "Control plane URL (default: http://localhost:8080)")
	cmd.Flags().StringVar(&email, "email", "", "Email address")
	cmd.Flags().StringVar(&password, "password", "", "Password")
	cmd.MarkFlagRequired("email")
	cmd.MarkFlagRequired("password")
	return cmd
}
