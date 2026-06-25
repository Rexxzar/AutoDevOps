package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func getCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <resource>",
		GroupID: groupDeploy,
		Short:   "List resources (deployments, clusters, containers, resources, compliance)",
	}
	cmd.AddCommand(
		getDeploymentsCmd(),
		getClustersCmd(),
		getContainersCmd(),
		getResourcesCmd(),
		getComplianceCmd(),
	)
	return cmd
}

func tw() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
}

func ageStr(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func getDeploymentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "deployments",
		Aliases: []string{"deployment", "deploy", "deploys"},
		Short:   "List all deployments",
		Example: "  autodev get deployments",
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

			if len(deployments) == 0 {
				fmt.Println("No deployments found.")
				return nil
			}

			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("TARGET")+"\t"+bold("STATUS")+"\t"+bold("FRAMEWORK")+"\t"+bold("AGE"))
			for _, d := range deployments {
				fw := d.Framework
				if fw == "" {
					fw = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					d.Name, d.TargetType, statusColor(d.Status), fw, ageStr(d.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
}

func getClustersCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "clusters",
		Aliases: []string{"cluster"},
		Short:   "List registered Kubernetes clusters",
		Example: "  autodev get clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			type cluster struct {
				ID        string    `json:"id"`
				Name      string    `json:"name"`
				Endpoint  string    `json:"endpoint"`
				Status    string    `json:"status"`
				CreatedAt time.Time `json:"created_at"`
			}
			var clusters []cluster
			if err := c.get(context.Background(), "/clusters", &clusters); err != nil {
				return err
			}

			if len(clusters) == 0 {
				fmt.Println("No clusters found.")
				return nil
			}

			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("STATUS")+"\t"+bold("ENDPOINT")+"\t"+bold("AGE"))
			for _, cl := range clusters {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					cl.Name, statusColor(cl.Status), cl.Endpoint, ageStr(cl.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
}

func getContainersCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "containers",
		Aliases: []string{"container"},
		Short:   "List container-target deployments",
		Example: "  autodev get containers",
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

			var containers []apiDeployment
			for _, d := range deployments {
				if d.TargetType == "container" {
					containers = append(containers, d)
				}
			}

			if len(containers) == 0 {
				fmt.Println("No container deployments found.")
				return nil
			}

			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("STATUS")+"\t"+bold("IMAGE")+"\t"+bold("AGE"))
			for _, d := range containers {
				img := d.ImageRef
				if img == "" {
					img = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					d.Name, statusColor(d.Status), img, ageStr(d.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
}

func getResourcesCmd() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:     "resources",
		Aliases: []string{"resource"},
		Short:   "List cloud resources (AWS + Azure)",
		Example: `  autodev get resources
  autodev get resources --provider aws`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			type resource struct {
				Provider     string    `json:"provider"`
				AccountName  string    `json:"account_name"`
				ResourceType string    `json:"resource_type"`
				ResourceID   string    `json:"resource_id"`
				Region       string    `json:"region"`
				Name         string    `json:"name"`
				State        string    `json:"state"`
				PolledAt     time.Time `json:"polled_at"`
			}
			var resources []resource
			if err := c.get(context.Background(), "/cloud/resources", &resources); err != nil {
				return err
			}

			if provider != "" {
				var filtered []resource
				for _, r := range resources {
					if strings.EqualFold(r.Provider, provider) {
						filtered = append(filtered, r)
					}
				}
				resources = filtered
			}

			if len(resources) == 0 {
				fmt.Println("No cloud resources found.")
				return nil
			}

			w := tw()
			fmt.Fprintln(w, bold("PROVIDER")+"\t"+bold("ACCOUNT")+"\t"+bold("TYPE")+"\t"+bold("NAME")+"\t"+bold("REGION")+"\t"+bold("STATE"))
			for _, r := range resources {
				name := r.Name
				if name == "" {
					name = r.ResourceID
				}
				state := stateFromJSON(r.State)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					strings.ToUpper(r.Provider), r.AccountName, r.ResourceType, name, r.Region, statusColor(state))
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Filter by provider (aws, azure)")
	return cmd
}

func stateFromJSON(raw string) string {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	for _, k := range []string{"state", "status", "power_state"} {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return "unknown"
}

func getComplianceCmd() *cobra.Command {
	var severity, scanner string

	cmd := &cobra.Command{
		Use:     "compliance",
		Aliases: []string{"findings", "finding"},
		Short:   "List compliance findings",
		Example: `  autodev get compliance
  autodev get compliance --severity critical
  autodev get compliance --scanner trivy`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			type finding struct {
				DeploymentName string `json:"deployment_name"`
				Scanner        string `json:"scanner"`
				Severity       string `json:"severity"`
				RuleID         string `json:"rule_id"`
				Message        string `json:"message"`
				FilePath       string `json:"file_path"`
				LineNumber     int    `json:"line_number"`
			}
			var findings []finding
			if err := c.get(context.Background(), "/compliance/findings", &findings); err != nil {
				return err
			}

			if severity != "" {
				var filtered []finding
				for _, f := range findings {
					if strings.EqualFold(f.Severity, severity) {
						filtered = append(filtered, f)
					}
				}
				findings = filtered
			}
			if scanner != "" {
				var filtered []finding
				for _, f := range findings {
					if strings.EqualFold(f.Scanner, scanner) {
						filtered = append(filtered, f)
					}
				}
				findings = filtered
			}

			if len(findings) == 0 {
				fmt.Println("No compliance findings.")
				return nil
			}

			// Summary counts
			counts := map[string]int{}
			for _, f := range findings {
				counts[f.Severity]++
			}
			fmt.Printf("  critical: %s  high: %s  medium: %s  low: %s\n\n",
				severityColor(fmt.Sprintf("%d", counts["critical"])),
				colorize(colorRed, fmt.Sprintf("%d", counts["high"])),
				colorize(colorYellow, fmt.Sprintf("%d", counts["medium"])),
				colorize(colorGray, fmt.Sprintf("%d", counts["low"])),
			)

			w := tw()
			fmt.Fprintln(w, bold("SEVERITY")+"\t"+bold("SCANNER")+"\t"+bold("DEPLOYMENT")+"\t"+bold("RULE")+"\t"+bold("MESSAGE"))
			for _, f := range findings {
				msg := f.Message
				if len(msg) > 60 {
					msg = msg[:57] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					severityColor(f.Severity), f.Scanner, f.DeploymentName, f.RuleID, msg)
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&severity, "severity", "", "Filter by severity (critical, high, medium, low, info)")
	cmd.Flags().StringVar(&scanner, "scanner", "", "Filter by scanner (hadolint, trivy, tfsec, gitleaks)")
	return cmd
}
