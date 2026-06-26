package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type cliLog struct {
	ID        int64          `json:"id"`
	Level     string         `json:"level"`
	Source    string         `json:"source"`
	Message   string         `json:"message"`
	Attrs     map[string]any `json:"attrs"`
	CreatedAt time.Time      `json:"created_at"`
}

func logsCmd() *cobra.Command {
	var level, source, search, since string
	var limit int
	var follow bool
	cmd := &cobra.Command{
		Use:     "logs",
		GroupID: groupPlatform,
		Short:   "View and tail the control-plane system log",
		Args:    cobra.NoArgs,
		Example: `  autodev logs --level ERROR
  autodev logs --source docker -f
  autodev logs -q restart --since 15m`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			if level != "" {
				q.Set("level", strings.ToUpper(level))
			}
			if source != "" {
				q.Set("source", source)
			}
			if search != "" {
				q.Set("q", search)
			}
			if since != "" {
				q.Set("since", since)
			}
			q.Set("limit", strconv.Itoa(limit))
			path := "/logs?" + q.Encode()

			var logs []cliLog
			if err := c.get(context.Background(), path, &logs); err != nil {
				return err
			}

			if jsonOutput() {
				return printJSON(logs)
			}

			// API returns newest-first; print chronologically (oldest at top).
			printChrono(logs)
			if !follow {
				return nil
			}

			// Follow: poll for entries newer than the highest id seen.
			var lastID int64
			for _, l := range logs {
				if l.ID > lastID {
					lastID = l.ID
				}
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					var batch []cliLog
					if err := c.get(ctx, path, &batch); err != nil {
						continue // transient; keep tailing
					}
					fresh := []cliLog{}
					for _, l := range batch {
						if l.ID > lastID {
							fresh = append(fresh, l)
						}
					}
					printChrono(fresh)
					for _, l := range fresh {
						if l.ID > lastID {
							lastID = l.ID
						}
					}
				}
			}
		},
	}
	cmd.Flags().StringVar(&level, "level", "", "filter: DEBUG|INFO|WARN|ERROR")
	cmd.Flags().StringVar(&source, "source", "", "filter by component (docker, k8s, deploy, ...)")
	cmd.Flags().StringVarP(&search, "search", "q", "", "substring match on the message")
	cmd.Flags().StringVar(&since, "since", "", "only logs newer than this (e.g. 15m, 2h)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max lines to fetch")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "tail the log (poll for new entries)")
	return cmd
}

func printChrono(logs []cliLog) {
	ordered := make([]cliLog, len(logs))
	copy(ordered, logs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, l := range ordered {
		printLogLine(l)
	}
}

func printLogLine(l cliLog) {
	ts := colorize(colorGray, l.CreatedAt.Local().Format("15:04:05"))
	lvl := levelColor(fmt.Sprintf("%-5s", l.Level))
	src := colorize(colorCyan, fmt.Sprintf("%-10s", truncate(l.Source, 10)))
	fmt.Printf("%s %s %s %s%s\n", ts, lvl, src, l.Message, attrsInline(l.Attrs))
}

func levelColor(l string) string {
	switch strings.TrimSpace(l) {
	case "ERROR":
		return colorize(colorRed, l)
	case "WARN":
		return colorize(colorYellow, l)
	case "INFO":
		return colorize(colorCyan, l)
	default:
		return colorize(colorGray, l)
	}
}

func attrsInline(attrs map[string]any) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, attrs[k]))
	}
	return colorize(colorGray, "  "+strings.Join(parts, " "))
}
