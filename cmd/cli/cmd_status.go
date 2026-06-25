package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

type apiDeployment struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RepoURL    string    `json:"repo_url"`
	Branch     string    `json:"branch"`
	Framework  string    `json:"framework"`
	TargetType string    `json:"target_type"`
	Status     string    `json:"status"`
	ImageRef   string    `json:"image_ref"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status <name>",
		GroupID: groupDeploy,
		Short:   "Show detailed status of a deployment",
		Args:    cobra.ExactArgs(1),
		Example: `  autodev status my-app`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			var deployments []apiDeployment
			if err := c.get(context.Background(), "/deployments", &deployments); err != nil {
				return err
			}

			name := args[0]
			var d *apiDeployment
			for i := range deployments {
				if deployments[i].Name == name {
					d = &deployments[i]
					break
				}
			}
			if d == nil {
				return fmt.Errorf("deployment %q not found", name)
			}

			age := time.Since(d.CreatedAt).Round(time.Minute)
			updated := time.Since(d.UpdatedAt).Round(time.Second)

			fmt.Printf("\n%s\n\n", bold(d.Name))
			fmt.Printf("  %-12s %s\n", "Status:", statusColor(d.Status))
			fmt.Printf("  %-12s %s\n", "Target:", d.TargetType)
			fmt.Printf("  %-12s %s\n", "Repo:", d.RepoURL)
			fmt.Printf("  %-12s %s\n", "Branch:", d.Branch)
			if d.Framework != "" {
				fmt.Printf("  %-12s %s\n", "Framework:", d.Framework)
			}
			if d.ImageRef != "" {
				fmt.Printf("  %-12s %s\n", "Image:", d.ImageRef)
			}
			fmt.Printf("  %-12s %s\n", "ID:", d.ID)
			fmt.Printf("  %-12s %s\n", "Age:", age.String())
			fmt.Printf("  %-12s %s ago\n", "Updated:", updated.String())
			fmt.Println()
			return nil
		},
	}
}
