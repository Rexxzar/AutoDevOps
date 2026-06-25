package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type cliFinding struct {
	Scanner        string `json:"scanner"`
	Severity       string `json:"severity"`
	RuleID         string `json:"rule_id"`
	Message        string `json:"message"`
	FilePath       string `json:"file_path"`
	LineNumber     int    `json:"line_number"`
	DeploymentName string `json:"deployment_name"`
}

func complianceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "compliance",
		Aliases: []string{"comp"},
		GroupID: groupPlatform,
		Short:   "Compliance findings (Hadolint, Trivy, tfsec, GitLeaks)",
	}
	cmd.AddCommand(complianceFindingsCmd(), complianceSummaryCmd())
	return cmd
}

func complianceFindingsCmd() *cobra.Command {
	var severity, scanner string
	cmd := &cobra.Command{
		Use:     "findings",
		Aliases: []string{"list", "ls"},
		Short:   "List compliance findings",
		Args:    cobra.NoArgs,
		Example: "  autodev compliance findings --severity critical\n  autodev compliance findings --scanner trivy",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var fs []cliFinding
			if err := c.get(context.Background(), "/compliance/findings", &fs); err != nil {
				return err
			}
			out := []cliFinding{}
			for _, f := range fs {
				if severity != "" && !strings.EqualFold(f.Severity, severity) {
					continue
				}
				if scanner != "" && !strings.EqualFold(f.Scanner, scanner) {
					continue
				}
				out = append(out, f)
			}
			if jsonOutput() {
				return printJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No findings.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("SEVERITY")+"\t"+bold("SCANNER")+"\t"+bold("RULE")+"\t"+bold("DEPLOYMENT")+"\t"+bold("MESSAGE"))
			for _, f := range out {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					severityColor(f.Severity), f.Scanner, f.RuleID, f.DeploymentName, truncate(f.Message, 60))
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&severity, "severity", "", "filter: critical|high|medium|low|info")
	cmd.Flags().StringVar(&scanner, "scanner", "", "filter: hadolint|trivy|tfsec|gitleaks")
	return cmd
}

func complianceSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Counts of findings by severity and scanner",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var fs []cliFinding
			if err := c.get(context.Background(), "/compliance/findings", &fs); err != nil {
				return err
			}
			bySev := map[string]int{}
			byScanner := map[string]int{}
			for _, f := range fs {
				bySev[f.Severity]++
				byScanner[f.Scanner]++
			}
			if jsonOutput() {
				return printJSON(map[string]any{"total": len(fs), "by_severity": bySev, "by_scanner": byScanner})
			}
			actionable := bySev["critical"] + bySev["high"]
			fmt.Printf("%s  %d total   %s\n", bold("Compliance"), len(fs),
				colorize(colorRed, fmt.Sprintf("%d actionable (critical+high)", actionable)))
			fmt.Println("  by severity:")
			for _, s := range []string{"critical", "high", "medium", "low", "info"} {
				if bySev[s] > 0 {
					fmt.Printf("    %s %d\n", severityColor(s), bySev[s])
				}
			}
			if len(byScanner) > 0 {
				fmt.Println("  by scanner:")
				for _, s := range []string{"hadolint", "trivy", "tfsec", "gitleaks"} {
					if byScanner[s] > 0 {
						fmt.Printf("    %-10s %d\n", s, byScanner[s])
					}
				}
			}
			return nil
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
