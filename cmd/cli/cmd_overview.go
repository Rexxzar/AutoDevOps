package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

type ovCluster struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type ovResource struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	State    string `json:"state"`
}

type ovFinding struct {
	Severity string `json:"severity"`
}

func overviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "overview",
		Aliases: []string{"dashboard", "summary"},
		GroupID: groupOther,
		Short:   "At-a-glance summary of deployments, clusters, cloud, and compliance",
		Args:    cobra.NoArgs,
		Example: "  autodev overview\n  autodev overview -o json",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			ctx := context.Background()

			// Best-effort fetches — a failing endpoint leaves its section empty
			// rather than aborting the whole overview.
			var deps []apiDeployment
			_ = c.get(ctx, "/deployments", &deps)
			var clusters []ovCluster
			_ = c.get(ctx, "/clusters", &clusters)
			var resources []ovResource
			_ = c.get(ctx, "/cloud/resources", &resources)
			var findings []ovFinding
			_ = c.get(ctx, "/compliance/findings", &findings)

			// Aggregate.
			depByStatus := map[string]int{}
			for _, d := range deps {
				depByStatus[d.Status]++
			}
			degraded := 0
			for _, cl := range clusters {
				if !isHealthyStatus(cl.Status) {
					degraded++
				}
			}
			aws, azure := 0, 0
			for _, r := range resources {
				switch strings.ToLower(r.Provider) {
				case "aws":
					aws++
				case "azure":
					azure++
				}
			}
			crit, high := 0, 0
			for _, f := range findings {
				switch f.Severity {
				case "critical":
					crit++
				case "high":
					high++
				}
			}

			if jsonOutput() {
				return printJSON(map[string]any{
					"deployments": map[string]any{"total": len(deps), "by_status": depByStatus},
					"clusters":    map[string]any{"total": len(clusters), "degraded": degraded},
					"cloud":       map[string]any{"total": len(resources), "aws": aws, "azure": azure},
					"compliance":  map[string]any{"total": len(findings), "critical": crit, "high": high},
				})
			}

			cfg, _ := loadConfig()
			server := cfg.ServerURL
			if flagServer != "" {
				server = flagServer
			}
			fmt.Printf("%s · %s\n\n", bold("AutoDevOps"), server)

			// Summary lines.
			fmt.Printf("%-13s %s   %s\n", "Deployments", bold(fmt.Sprint(len(deps))), depSummary(depByStatus))
			clusterSub := colorize(colorGreen, "all healthy")
			if degraded > 0 {
				clusterSub = colorize(colorYellow, fmt.Sprintf("%d degraded", degraded))
			} else if len(clusters) == 0 {
				clusterSub = colorize(colorGray, "none registered")
			}
			fmt.Printf("%-13s %s   %s\n", "Clusters", bold(fmt.Sprint(len(clusters))), clusterSub)
			fmt.Printf("%-13s %s   %s\n", "Cloud", bold(fmt.Sprint(len(resources))), fmt.Sprintf("%d AWS · %d Azure", aws, azure))
			fmt.Printf("%-13s %s findings   %s\n", "Compliance", bold(fmt.Sprint(len(findings))), complianceSub(crit, high))

			// Recent deployments (newest 5).
			if len(deps) > 0 {
				sort.Slice(deps, func(i, j int) bool { return deps[i].CreatedAt.After(deps[j].CreatedAt) })
				fmt.Printf("\n%s\n", bold("Recent deployments"))
				w := tw()
				for i, d := range deps {
					if i >= 5 {
						break
					}
					fw := d.Framework
					if fw == "" {
						fw = "—"
					}
					fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", d.Name, statusColor(d.Status), fw, ageStr(d.CreatedAt))
				}
				w.Flush()
			}

			// Cluster health.
			if len(clusters) > 0 {
				fmt.Printf("\n%s\n", bold("Cluster health"))
				w := tw()
				for _, cl := range clusters {
					fmt.Fprintf(w, "  %s\t%s\n", cl.Name, statusColor(cl.Status))
				}
				w.Flush()
			}
			return nil
		},
	}
}

func isHealthyStatus(s string) bool {
	switch s {
	case "healthy", "connected", "available", "running":
		return true
	}
	return false
}

func depSummary(byStatus map[string]int) string {
	parts := []string{}
	for _, k := range []string{"running", "building", "failed"} {
		parts = append(parts, fmt.Sprintf("%d %s", byStatus[k], k))
	}
	return strings.Join(parts, " · ")
}

func complianceSub(crit, high int) string {
	if crit == 0 && high == 0 {
		return colorize(colorGreen, "no critical/high")
	}
	return fmt.Sprintf("%s · %s",
		colorize(colorRed, fmt.Sprintf("%d critical", crit)),
		colorize(colorYellow, fmt.Sprintf("%d high", high)))
}
