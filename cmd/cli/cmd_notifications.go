package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

type cliNotification struct {
	ID        string    `json:"id"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}

func notificationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "notifications",
		Aliases: []string{"notif", "notifications"},
		GroupID: groupPlatform,
		Short:   "View and manage notifications",
	}
	cmd.AddCommand(notificationsListCmd(), notificationsReadCmd())
	return cmd
}

func notificationsListCmd() *cobra.Command {
	var unread bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List recent notifications",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var ns []cliNotification
			if err := c.get(context.Background(), "/notifications?limit=50", &ns); err != nil {
				return err
			}
			out := []cliNotification{}
			for _, n := range ns {
				if unread && n.Read {
					continue
				}
				out = append(out, n)
			}
			if jsonOutput() {
				return printJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No notifications.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("")+"\t"+bold("SEVERITY")+"\t"+bold("TITLE")+"\t"+bold("WHEN"))
			for _, n := range out {
				dot := " "
				if !n.Read {
					dot = colorize(colorCyan, "●")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", dot, severityDot(n.Severity), n.Title, ageStr(n.CreatedAt)+" ago")
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().BoolVar(&unread, "unread", false, "show only unread")
	return cmd
}

func notificationsReadCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "read [id...]",
		Short:   "Mark notifications as read (--all or specific ids)",
		Example: "  autodev notifications read --all",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			if !all && len(args) == 0 {
				return fmt.Errorf("pass --all or one or more notification ids")
			}
			body := map[string]any{}
			if all {
				body["all"] = true
			} else {
				body["ids"] = args
			}
			if err := c.post(context.Background(), "/notifications/read", body, nil); err != nil {
				return err
			}
			success("Marked read.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "mark all notifications read")
	return cmd
}

// severityDot maps a notification severity to a colored label.
func severityDot(sev string) string {
	switch sev {
	case "critical":
		return colorize(colorRed, sev)
	case "warning":
		return colorize(colorYellow, sev)
	case "success":
		return colorize(colorGreen, sev)
	default:
		return colorize(colorCyan, sev)
	}
}
