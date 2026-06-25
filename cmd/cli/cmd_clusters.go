package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cliCluster struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type cliPod struct {
	ClusterID    string `json:"cluster_id"`
	Namespace    string `json:"namespace"`
	PodName      string `json:"pod_name"`
	Status       string `json:"status"`
	RestartCount int32  `json:"restart_count"`
}

func clustersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "clusters",
		Aliases: []string{"cluster"},
		GroupID: groupPlatform,
		Short:   "Kubernetes clusters and pod health",
	}
	cmd.AddCommand(clustersListCmd(), clustersPodsCmd(), clustersRegisterCmd())
	return cmd
}

func clustersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List registered Kubernetes clusters",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var clusters []cliCluster
			if err := c.get(context.Background(), "/clusters", &clusters); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(clusters)
			}
			if len(clusters) == 0 {
				fmt.Println("No clusters registered.")
				return nil
			}
			// Pod counts per cluster (best-effort).
			var pods []cliPod
			_ = c.get(context.Background(), "/clusters/pods", &pods)
			counts := map[string]int{}
			for _, p := range pods {
				counts[p.ClusterID]++
			}
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("STATUS")+"\t"+bold("PODS")+"\t"+bold("ENDPOINT")+"\t"+bold("AGE"))
			for _, cl := range clusters {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", cl.Name, statusColor(cl.Status), counts[cl.ID], cl.Endpoint, ageStr(cl.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
}

func clustersPodsCmd() *cobra.Command {
	var namespace, cluster, status string
	cmd := &cobra.Command{
		Use:     "pods",
		Short:   "List pod health (status + restart counts)",
		Args:    cobra.NoArgs,
		Example: "  autodev clusters pods\n  autodev clusters pods --namespace default --status unhealthy",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			// Resolve --cluster name → id.
			clusterID := ""
			if cluster != "" {
				var clusters []cliCluster
				if err := c.get(context.Background(), "/clusters", &clusters); err != nil {
					return err
				}
				for _, cl := range clusters {
					if cl.Name == cluster || cl.ID == cluster {
						clusterID = cl.ID
						break
					}
				}
				if clusterID == "" {
					return fmt.Errorf("cluster %q not found", cluster)
				}
			}
			var pods []cliPod
			if err := c.get(context.Background(), "/clusters/pods", &pods); err != nil {
				return err
			}
			out := []cliPod{}
			for _, p := range pods {
				if namespace != "" && p.Namespace != namespace {
					continue
				}
				if clusterID != "" && p.ClusterID != clusterID {
					continue
				}
				if status != "" && !strings.EqualFold(p.Status, status) {
					continue
				}
				out = append(out, p)
			}
			if jsonOutput() {
				return printJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No pods found.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("NAMESPACE")+"\t"+bold("POD")+"\t"+bold("STATUS")+"\t"+bold("RESTARTS"))
			for _, p := range out {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", p.Namespace, p.PodName, statusColor(p.Status), p.RestartCount)
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "filter by namespace")
	cmd.Flags().StringVar(&cluster, "cluster", "", "filter by cluster name|id")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (healthy|unhealthy|pending|...)")
	return cmd
}

func clustersRegisterCmd() *cobra.Command {
	var name, endpoint string
	cmd := &cobra.Command{
		Use:     "register",
		Short:   "Register a Kubernetes cluster",
		Args:    cobra.NoArgs,
		Example: "  autodev clusters register --name kind --endpoint https://127.0.0.1:6443",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || endpoint == "" {
				return fmt.Errorf("--name and --endpoint are required")
			}
			c, err := requireClient()
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/clusters", map[string]string{"name": name, "endpoint": endpoint}, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Registered cluster %s", bold(name)))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "cluster name")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "API server endpoint URL")
	return cmd
}
