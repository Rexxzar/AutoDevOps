package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func destroyCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:     "destroy <name>",
		GroupID: groupDeploy,
		Short:   "Destroy a deployment",
		Args:    cobra.ExactArgs(1),
		Example: `  autodev destroy my-app
  autodev destroy my-app --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			// Find deployment by name
			var deployments []apiDeployment
			if err := c.get(context.Background(), "/deployments", &deployments); err != nil {
				return err
			}
			name := args[0]
			var deployID string
			for _, d := range deployments {
				if d.Name == name {
					deployID = d.ID
					break
				}
			}
			if deployID == "" {
				return fmt.Errorf("deployment %q not found", name)
			}

			if !yes {
				fmt.Printf("  Destroy deployment %s? This cannot be undone. [y/N] ", bold(name))
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				if !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
					fmt.Println("  Aborted.")
					return nil
				}
			}

			if err := c.delete(context.Background(), "/deployments/"+deployID); err != nil {
				return fmt.Errorf("destroy: %w", err)
			}
			success(fmt.Sprintf("Deployment %q destroyed", name))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}
