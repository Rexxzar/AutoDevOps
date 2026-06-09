package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "autodev",
		Short: "AutoDevOps control-plane CLI",
		Long: `autodev — interact with the AutoDevOps control plane.

  autodev login       Authenticate and save credentials
  autodev deploy      Register a Git repo for deployment
  autodev get         List resources (deployments, clusters, containers, resources, compliance)
  autodev status      Show detailed status of a deployment
  autodev logs        Stream live events for a deployment
  autodev destroy     Destroy a deployment`,
		SilenceUsage: true,
	}

	root.AddCommand(
		loginCmd(),
		deployCmd(),
		getCmd(),
		statusCmd(),
		logsCmd(),
		destroyCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
