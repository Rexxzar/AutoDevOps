package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

type cliWorkspace struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Provider     string    `json:"provider"`
	ResourceType string    `json:"resource_type"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	HasState     bool      `json:"has_state"`
	LastRun      string    `json:"last_run"`
	RunCount     int       `json:"run_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type cliTfRun struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resource_id"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

func terraformCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "terraform",
		Aliases: []string{"tf"},
		GroupID: groupPlatform,
		Short:   "Terraform store — workspaces and run history",
	}
	cmd.AddCommand(terraformWorkspacesCmd(), terraformRunsCmd())
	return cmd
}

func terraformWorkspacesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "workspaces",
		Aliases: []string{"ws", "workspace"},
		Short:   "List Terraform workspaces (one per provisioned resource)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var ws []cliWorkspace
			if err := c.get(context.Background(), "/terraform/workspaces", &ws); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(ws)
			}
			if len(ws) == 0 {
				fmt.Println("No terraform workspaces.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("PROVIDER")+"\t"+bold("TYPE")+"\t"+bold("KIND")+"\t"+bold("STATE")+"\t"+bold("RUNS")+"\t"+bold("LAST RUN"))
			for _, x := range ws {
				state := colorize(colorGray, "none")
				if x.HasState {
					state = colorize(colorGreen, "yes")
				}
				lr := x.LastRun
				if lr == "" {
					lr = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", x.Name, x.Provider, x.ResourceType, x.Kind, state, x.RunCount, lr)
			}
			w.Flush()
			return nil
		},
	}
}

func terraformRunsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "runs",
		Short: "List recent Terraform runs (plan/apply/destroy)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var runs []cliTfRun
			if err := c.get(context.Background(), "/terraform/runs", &runs); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(runs)
			}
			if len(runs) == 0 {
				fmt.Println("No terraform runs.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("ACTION")+"\t"+bold("STATUS")+"\t"+bold("WHEN"))
			for _, r := range runs {
				fmt.Fprintf(w, "%s\t%s\t%s\n", r.Action, statusColor(r.Status), ageStr(r.CreatedAt)+" ago")
			}
			w.Flush()
			return nil
		},
	}
}
