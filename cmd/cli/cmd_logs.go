package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "logs <name>",
		Short:   "Stream live events for a deployment",
		Args:    cobra.ExactArgs(1),
		Example: `  autodev logs my-app`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			c := newClient(cfg)

			// Resolve name → ID
			var deployments []apiDeployment
			if err := c.get(context.Background(), "/deployments", &deployments); err != nil {
				return err
			}
			name := args[0]
			var deployID string
			for _, d := range deployments {
				if d.Name == name {
					deployID = d.ID
					break
				}
			}
			if deployID == "" {
				return fmt.Errorf("deployment %q not found", name)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			fmt.Printf("%s Streaming events for %s (Ctrl+C to stop)\n\n",
				colorize(colorCyan, "→"), bold(name))

			return c.streamSSE(ctx, "/events", func(ev SSEEvent) {
				// Filter to events relevant to this deployment
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
					return
				}

				// Check if this event belongs to our deployment
				relevant := false
				if id, ok := payload["deployment_id"].(string); ok && id == deployID {
					relevant = true
				}
				if id, ok := payload["id"].(string); ok && id == deployID {
					relevant = true
				}

				switch ev.Type {
				case "pod.health", "pod.heal.restart", "pod.heal.gaveup",
					"container.health":
					// Only show if deployment name matches
					if n, ok := payload["deployment_name"].(string); ok && n == name {
						relevant = true
					}
				}

				if !relevant {
					return
				}

				ts := time.Now().Format("15:04:05")
				prefix := colorize(colorGray, ts)

				switch ev.Type {
				case "deploy.status":
					status, _ := payload["status"].(string)
					msg, _ := payload["message"].(string)
					fmt.Printf("%s  %s  %s\n", prefix, statusColor(status), msg)

				case "compliance.finding":
					sev, _ := payload["severity"].(string)
					rule, _ := payload["rule_id"].(string)
					message, _ := payload["message"].(string)
					fmt.Printf("%s  %s [%s] %s\n", prefix, severityColor(sev), rule, message)

				case "pod.health":
					pod, _ := payload["pod_name"].(string)
					phase, _ := payload["phase"].(string)
					fmt.Printf("%s  pod/%s → %s\n", prefix, pod, statusColor(phase))

				case "pod.heal.restart":
					pod, _ := payload["pod_name"].(string)
					attempt, _ := payload["attempt"].(float64)
					fmt.Printf("%s  %s pod/%s restarted (attempt %d)\n",
						prefix, colorize(colorYellow, "↻"), pod, int(attempt))

				case "pod.heal.gaveup":
					pod, _ := payload["pod_name"].(string)
					fmt.Printf("%s  %s pod/%s — healer gave up\n",
						prefix, colorize(colorRed, "✗"), pod)

				case "container.health":
					status, _ := payload["status"].(string)
					fmt.Printf("%s  container → %s\n", prefix, statusColor(status))

				default:
					fmt.Printf("%s  [%s] %s\n", prefix, ev.Type, ev.Data)
				}
			})
		},
	}
}
