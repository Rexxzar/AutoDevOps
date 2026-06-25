package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type cliCloudResource struct {
	Provider     string `json:"provider"`
	AccountName  string `json:"account_name"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Region       string `json:"region"`
	Name         string `json:"name"`
	State        string `json:"state"`
}

func inventoryCmd() *cobra.Command {
	var provider string
	cmd := &cobra.Command{
		Use:     "inventory",
		Aliases: []string{"inv"},
		GroupID: groupResources,
		Short:   "List live cloud inventory (polled AWS + Azure resources)",
		Args:    cobra.NoArgs,
		Example: "  autodev inventory\n  autodev inventory --provider aws",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var rs []cliCloudResource
			if err := c.get(context.Background(), "/cloud/resources", &rs); err != nil {
				return err
			}
			out := []cliCloudResource{}
			for _, r := range rs {
				if provider != "" && !strings.EqualFold(r.Provider, provider) {
					continue
				}
				out = append(out, r)
			}
			if jsonOutput() {
				return printJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No cloud inventory found.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("PROVIDER")+"\t"+bold("ACCOUNT")+"\t"+bold("TYPE")+"\t"+bold("NAME")+"\t"+bold("REGION")+"\t"+bold("STATE"))
			for _, r := range out {
				name := r.Name
				if name == "" {
					name = r.ResourceID
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					strings.ToUpper(r.Provider), r.AccountName, r.ResourceType, name, r.Region, statusColor(stateFromJSON(r.State)))
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "filter by provider: aws | azure")
	return cmd
}

type cliInventory struct {
	DockerAvailable bool `json:"docker_available"`
	K8sAvailable    bool `json:"k8s_available"`
	Containers      []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Image  string `json:"image"`
		State  string `json:"state"`
		Status string `json:"status"`
	} `json:"containers"`
	Pods []struct {
		ID        string `json:"id"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Phase     string `json:"phase"`
	} `json:"pods"`
}

func localCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "local",
		GroupID: groupResources,
		Short:   "Inspect local Docker containers & K8s pods (live inventory)",
	}
	cmd.AddCommand(localListCmd(), localLogsCmd(), localRestartCmd())
	return cmd
}

func localListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List local containers and pods",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var inv cliInventory
			if err := c.get(context.Background(), "/local/inventory", &inv); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(inv)
			}
			if len(inv.Containers) > 0 {
				fmt.Println(bold("Containers"))
				w := tw()
				fmt.Fprintln(w, "  "+bold("NAME")+"\t"+bold("IMAGE")+"\t"+bold("STATE")+"\t"+bold("STATUS"))
				for _, ct := range inv.Containers {
					fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", ct.Name, ct.Image, statusColor(ct.State), ct.Status)
				}
				w.Flush()
			}
			if len(inv.Pods) > 0 {
				fmt.Println(bold("\nPods"))
				w := tw()
				fmt.Fprintln(w, "  "+bold("NAMESPACE")+"\t"+bold("NAME")+"\t"+bold("PHASE"))
				for _, p := range inv.Pods {
					fmt.Fprintf(w, "  %s\t%s\t%s\n", p.Namespace, p.Name, statusColor(strings.ToLower(p.Phase)))
				}
				w.Flush()
			}
			if len(inv.Containers) == 0 && len(inv.Pods) == 0 {
				fmt.Println("No local containers or pods found.")
			}
			return nil
		},
	}
}

func localLogsCmd() *cobra.Command {
	var tail int
	cmd := &cobra.Command{
		Use:     "logs <name|id>",
		Short:   "Show recent logs for a local container or pod",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev local logs shophup-front --tail 100",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveLocalID(c, args[0])
			if err != nil {
				return err
			}
			var resp struct {
				Logs string `json:"logs"`
			}
			path := fmt.Sprintf("/local/inventory/%s/logs?tail=%d", id, tail)
			if err := c.get(context.Background(), path, &resp); err != nil {
				return err
			}
			fmt.Print(resp.Logs)
			return nil
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 100, "number of log lines to show")
	return cmd
}

func localRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name|id>",
		Short: "Restart a local container or pod",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveLocalID(c, args[0])
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/local/inventory/"+id+"/restart", nil, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Restarted %s", args[0]))
			return nil
		},
	}
}

// resolveLocalID accepts a synthetic inventory id ("docker:..."/"k8s:...") as-is,
// or resolves a plain container/pod name to one via the inventory listing.
func resolveLocalID(c *Client, arg string) (string, error) {
	if strings.HasPrefix(arg, "docker:") || strings.HasPrefix(arg, "k8s:") {
		return arg, nil
	}
	var inv cliInventory
	if err := c.get(context.Background(), "/local/inventory", &inv); err != nil {
		return "", err
	}
	var matches []string
	for _, ct := range inv.Containers {
		if ct.Name == arg {
			matches = append(matches, ct.ID)
		}
	}
	for _, p := range inv.Pods {
		if p.Name == arg {
			matches = append(matches, p.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no local container/pod named %q (try: autodev local list)", arg)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple matches for %q — use the id from: autodev local list", arg)
	}
}
