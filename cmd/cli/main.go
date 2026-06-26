package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "autodev",
		Short: "AutoDevOps control-plane CLI",
		Long: `autodev — manage deployments, cloud resources, and the platform from your terminal.

The CLI talks to the same control plane as the dashboard, so anything you can do
in the UI you can script here. Configure the server and credentials once with
"autodev login"; override per-command with --server/--token.`,
		Example: `  autodev login
  autodev get deployments
  autodev get deployments -o json
  autodev status my-app`,
		Version: version,
		// We print errors ourselves (fatal, below); no usage spam on failures.
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if flagOutput != "table" && flagOutput != "json" {
				return fmt.Errorf("invalid --output %q (use: table | json)", flagOutput)
			}
			return nil
		},
	}

	root.SetVersionTemplate("autodev {{.Version}}\n")

	// Global flags — config is the default; these override it for one invocation.
	pf := root.PersistentFlags()
	pf.StringVar(&flagServer, "server", "", "control-plane URL (overrides config)")
	pf.StringVar(&flagToken, "token", "", "auth token (overrides config)")
	pf.StringVarP(&flagOutput, "output", "o", "table", "output format: table | json")
	pf.BoolVar(&flagNoColor, "no-color", false, "disable colored output")

	// Grouped help — commands list under these headings instead of one flat block.
	// Groups are added lazily as sections land (so no empty headings show).
	root.AddGroup(
		&cobra.Group{ID: groupAuth, Title: "Auth:"},
		&cobra.Group{ID: groupDeploy, Title: "Deployments:"},
		&cobra.Group{ID: groupResources, Title: "Resources:"},
		&cobra.Group{ID: groupPlatform, Title: "Platform:"},
		&cobra.Group{ID: groupOther, Title: "Other:"},
	)
	root.SetHelpCommandGroupID(groupOther)
	root.SetCompletionCommandGroupID(groupOther)

	root.AddCommand(
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		deploymentsCmd(),
		getCmd(),
		resourcesCmd(),
		inventoryCmd(),
		localCmd(),
		cloudCmd(),
		clustersCmd(),
		healCmd(),
		complianceCmd(),
		logsCmd(),
		secretsCmd(),
		notificationsCmd(),
		reposCmd(),
		terraformCmd(),
		imagesCmd(),
		overviewCmd(),
		infoCmd(),
		versionCmd(),
	)

	if err := root.Execute(); err != nil {
		fatal(err.Error())
	}
}
