package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type cliHealKind struct {
	Total          int      `json:"total"`
	Recovered      int      `json:"recovered"`
	Unrecovered    int      `json:"unrecovered"`
	RecoveryRate   float64  `json:"recovery_rate"`
	AvgMTTDSeconds *float64 `json:"avg_mttd_seconds"`
	AvgMTTRSeconds *float64 `json:"avg_mttr_seconds"`
}

type cliHealMetrics struct {
	Overall cliHealKind            `json:"overall"`
	ByKind  map[string]cliHealKind `json:"by_kind"`
}

type cliHealEpisode struct {
	TargetKind  string     `json:"target_kind"`
	TargetName  string     `json:"target_name"`
	Reason      string     `json:"reason"`
	DetectedAt  time.Time  `json:"detected_at"`
	RecoveredAt *time.Time `json:"recovered_at"`
	GaveUp      bool       `json:"gave_up"`
	MTTRSeconds *float64   `json:"mttr_seconds"`
}

func healCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "heal",
		GroupID: groupPlatform,
		Short:   "Self-healing metrics, episodes, and live stream (pods + containers)",
	}
	cmd.AddCommand(healMetricsCmd(), healEpisodesCmd(), healStreamCmd())
	return cmd
}

func fmtSecs(s *float64) string {
	if s == nil {
		return "—"
	}
	v := *s
	if v < 1 {
		return "<1s"
	}
	if v < 60 {
		return fmt.Sprintf("%.0fs", v)
	}
	return fmt.Sprintf("%dm %ds", int(v)/60, int(v)%60)
}

func healMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Self-healing recovery rate, MTTD, and MTTR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var m cliHealMetrics
			if err := c.get(context.Background(), "/heal/metrics", &m); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(m)
			}
			o := m.Overall
			fmt.Println(bold("Self-healing metrics"))
			rate := "—"
			if o.Total > 0 {
				rate = fmt.Sprintf("%.0f%%", o.RecoveryRate*100)
			}
			fmt.Printf("  Recovery rate   %s   (%d/%d recovered)\n", rate, o.Recovered, o.Total)
			fmt.Printf("  Avg MTTD        %s\n", fmtSecs(o.AvgMTTDSeconds))
			fmt.Printf("  Avg MTTR        %s\n", fmtSecs(o.AvgMTTRSeconds))
			if len(m.ByKind) > 0 {
				fmt.Println("  by kind:")
				for _, k := range []string{"pod", "container"} {
					km, ok := m.ByKind[k]
					if !ok || km.Total == 0 {
						continue
					}
					fmt.Printf("    %-10s %d episodes  %.0f%% recovered  MTTR %s\n",
						k, km.Total, km.RecoveryRate*100, fmtSecs(km.AvgMTTRSeconds))
				}
			}
			return nil
		},
	}
}

func healEpisodesCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "episodes",
		Short:   "List recent self-healing episodes",
		Args:    cobra.NoArgs,
		Example: "  autodev heal episodes --limit 20",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var eps []cliHealEpisode
			if err := c.get(context.Background(), "/heal/episodes?limit="+strconv.Itoa(limit), &eps); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(eps)
			}
			if len(eps) == 0 {
				fmt.Println("No healing episodes recorded yet.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("KIND")+"\t"+bold("TARGET")+"\t"+bold("DETECTED")+"\t"+bold("OUTCOME")+"\t"+bold("MTTR"))
			for _, e := range eps {
				outcome := colorize(colorYellow, "healing…")
				if e.RecoveredAt != nil {
					outcome = colorize(colorGreen, "recovered")
				} else if e.GaveUp {
					outcome = colorize(colorRed, "gave up")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.TargetKind, e.TargetName, ageStr(e.DetectedAt)+" ago", outcome, fmtSecs(e.MTTRSeconds))
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 30, "max episodes to show")
	return cmd
}

func healStreamCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stream",
		Short: "Live stream of self-heal events (pods + containers)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			fmt.Printf("%s Streaming self-heal events (Ctrl+C to stop)\n\n", colorize(colorCyan, "→"))

			return c.streamSSE(ctx, "/events", func(ev SSEEvent) {
				var p map[string]any
				if json.Unmarshal([]byte(ev.Data), &p) != nil {
					return
				}
				ts := colorize(colorGray, time.Now().Format("15:04:05"))
				switch ev.Type {
				case "pod.heal.restart":
					fmt.Printf("%s  %s pod/%s restarted (attempt %s) — %s\n", ts, colorize(colorYellow, "↻"), str(p["pod_name"]), numStr(p["attempt"]), str(p["reason"]))
				case "pod.heal.gaveup":
					fmt.Printf("%s  %s pod/%s — healer gave up\n", ts, colorize(colorRed, "✗"), str(p["pod_name"]))
				case "container.heal.restart":
					fmt.Printf("%s  %s container/%s restarted (attempt %s) — %s\n", ts, colorize(colorYellow, "↻"), str(p["name"]), numStr(p["attempt"]), str(p["reason"]))
				case "container.heal.gaveup":
					fmt.Printf("%s  %s container/%s — healer gave up\n", ts, colorize(colorRed, "✗"), str(p["name"]))
				}
			})
		},
	}
}

func numStr(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.Itoa(int(f))
	}
	return "?"
}
