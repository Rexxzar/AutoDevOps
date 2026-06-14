package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func deployCmd() *cobra.Command {
	var repoURL, name, branch, target string
	var trigger bool

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Register a Git repo for deployment",
		Example: `  autodev deploy --name my-app --repo https://github.com/you/my-app --target k8s
  autodev deploy --name my-api --repo https://github.com/you/api --target container --trigger`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			body := map[string]string{
				"name":        name,
				"repo_url":    repoURL,
				"branch":      branch,
				"target_type": target,
			}

			var resp struct {
				ID            string `json:"id"`
				WebhookURL    string `json:"webhook_url"`
				WebhookSecret string `json:"webhook_secret"`
			}
			if err := c.post(context.Background(), "/deployments", body, &resp); err != nil {
				return fmt.Errorf("create deployment: %w", err)
			}

			success(fmt.Sprintf("Deployment %q registered (id: %s)", name, resp.ID))
			fmt.Println()
			fmt.Println(bold("  GitHub Webhook Setup"))
			fmt.Printf("  URL:    %s%s\n", cfg.ServerURL, resp.WebhookURL)
			fmt.Printf("  Secret: %s\n", resp.WebhookSecret)
			fmt.Println("  Event:  push")
			fmt.Println()

			if trigger {
				fmt.Printf("  Triggering build...\n")
				if err := c.post(context.Background(), "/deployments/"+resp.ID+"/trigger", nil, nil); err != nil {
					return fmt.Errorf("trigger: %w", err)
				}
				success("Build triggered — run `autodev logs " + name + "` to follow progress")
			} else {
				fmt.Println("  Push to GitHub to trigger the first build, or run:")
				fmt.Printf("  autodev deploy --trigger  (re-run with same flags to build immediately)\n")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Deployment name")
	cmd.Flags().StringVar(&repoURL, "repo", "", "Git repository URL")
	cmd.Flags().StringVar(&branch, "branch", "main", "Branch to deploy")
	cmd.Flags().StringVar(&target, "target", "k8s", "Deployment target: k8s, container")
	cmd.Flags().BoolVar(&trigger, "trigger", false, "Trigger a build immediately after registration")
	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("repo")
	return cmd
}
