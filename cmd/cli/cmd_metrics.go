package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

type cliDeployRun struct {
	AppName         string    `json:"app_name"`
	TargetKind      string    `json:"target_kind"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	DurationSeconds *float64  `json:"duration_seconds"`
}

type cliProvisionRun struct {
	Template        string    `json:"template"`
	Action          string    `json:"action"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	DurationSeconds *float64  `json:"duration_seconds"`
}

type cliHealMetric struct {
	TargetKind  string   `json:"target_kind"`
	TargetName  string   `json:"target_name"`
	Reason      string   `json:"reason"`
	GaveUp      bool     `json:"gave_up"`
	MTTDSeconds *float64 `json:"mttd_seconds"`
	MTTRSeconds *float64 `json:"mttr_seconds"`
}

// metricsCmd groups the Chapter 5 evaluation exports: end-to-end deploy latency,
// provisioning latency, and self-healing recovery. Every subcommand supports
// -o json and -o csv, so the data drops straight into the evaluation analysis.
func metricsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "metrics",
		GroupID: groupPlatform,
		Short:   "Evaluation metrics: deploy latency, provisioning latency, self-healing",
	}
	cmd.AddCommand(metricsDeploysCmd(), metricsProvisionCmd(), metricsHealCmd())
	return cmd
}

func metricsDeploysCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "deploys",
		Short:   "End-to-end deploy latency, one row per pipeline run",
		Args:    cobra.NoArgs,
		Example: "  autodev metrics deploys\n  autodev metrics deploys -o csv > deploys.csv",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var rs []cliDeployRun
			if err := c.get(context.Background(), "/metrics/deploys", &rs); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(rs)
			}
			if csvOutput() {
				rows := make([][]string, 0, len(rs))
				for _, r := range rs {
					rows = append(rows, []string{r.AppName, r.TargetKind, r.Status, r.StartedAt.Format(time.RFC3339), csvSecs(r.DurationSeconds)})
				}
				return printCSV([]string{"app", "target", "status", "started_at", "duration_s"}, rows)
			}
			if len(rs) == 0 {
				fmt.Println("No deploy runs recorded yet.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("APP")+"\t"+bold("TARGET")+"\t"+bold("STATUS")+"\t"+bold("STARTED")+"\t"+bold("DURATION"))
			for _, r := range rs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.AppName, r.TargetKind, statusColor(r.Status), ageStr(r.StartedAt)+" ago", dispSecs(r.DurationSeconds))
			}
			w.Flush()
			return nil
		},
	}
}

func metricsProvisionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "provision",
		Short:   "Provisioning latency, one row per terraform apply/destroy",
		Args:    cobra.NoArgs,
		Example: "  autodev metrics provision -o csv > provision.csv",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var rs []cliProvisionRun
			if err := c.get(context.Background(), "/metrics/provision", &rs); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(rs)
			}
			if csvOutput() {
				rows := make([][]string, 0, len(rs))
				for _, r := range rs {
					rows = append(rows, []string{r.Template, r.Action, r.Status, r.CreatedAt.Format(time.RFC3339), csvSecs(r.DurationSeconds)})
				}
				return printCSV([]string{"template", "action", "status", "created_at", "duration_s"}, rows)
			}
			if len(rs) == 0 {
				fmt.Println("No provisioning runs recorded yet.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("TEMPLATE")+"\t"+bold("ACTION")+"\t"+bold("STATUS")+"\t"+bold("WHEN")+"\t"+bold("DURATION"))
			for _, r := range rs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Template, r.Action, statusColor(r.Status), ageStr(r.CreatedAt)+" ago", dispSecs(r.DurationSeconds))
			}
			w.Flush()
			return nil
		},
	}
}

func metricsHealCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "heal",
		Short:   "Self-healing episodes with MTTD and MTTR",
		Args:    cobra.NoArgs,
		Example: "  autodev metrics heal -o csv > heal.csv",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var rs []cliHealMetric
			if err := c.get(context.Background(), "/heal/episodes", &rs); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(rs)
			}
			if csvOutput() {
				rows := make([][]string, 0, len(rs))
				for _, r := range rs {
					rows = append(rows, []string{r.TargetKind, r.TargetName, r.Reason, strconv.FormatBool(r.GaveUp), csvSecs(r.MTTDSeconds), csvSecs(r.MTTRSeconds)})
				}
				return printCSV([]string{"kind", "target", "reason", "gave_up", "mttd_s", "mttr_s"}, rows)
			}
			if len(rs) == 0 {
				fmt.Println("No self-healing episodes recorded yet.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("KIND")+"\t"+bold("TARGET")+"\t"+bold("MTTD")+"\t"+bold("MTTR")+"\t"+bold("GAVE UP"))
			for _, r := range rs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", r.TargetKind, r.TargetName, dispSecs(r.MTTDSeconds), dispSecs(r.MTTRSeconds), r.GaveUp)
			}
			w.Flush()
			return nil
		},
	}
}

// printCSV writes header + rows to stdout as CSV.
func printCSV(header []string, rows [][]string) error {
	w := csv.NewWriter(os.Stdout)
	if err := w.Write(header); err != nil {
		return err
	}
	w.WriteAll(rows) // flushes
	return w.Error()
}

// csvSecs formats a nullable seconds value for CSV: empty when nil, else a plain
// decimal with no unit.
func csvSecs(s *float64) string {
	if s == nil {
		return ""
	}
	return strconv.FormatFloat(*s, 'f', 2, 64)
}

// dispSecs formats a nullable seconds value for the human table.
func dispSecs(s *float64) string {
	if s == nil {
		return "-"
	}
	return strconv.FormatFloat(*s, 'f', 1, 64) + "s"
}
