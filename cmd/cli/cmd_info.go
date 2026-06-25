package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

type systemInfo struct {
	Version       string          `json:"version"`
	Subsystems    map[string]bool `json:"subsystems"`
	IngressDomain string          `json:"ingress_domain"`
	RegistryHost  string          `json:"registry_host"`
	PublicIP      string          `json:"public_ip"`
}

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "info",
		GroupID: groupOther,
		Short:   "Show CLI + control-plane info, health, and subsystems",
		Args:    cobra.NoArgs,
		Example: "  autodev info\n  autodev info -o json",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if flagServer != "" {
				cfg.ServerURL = flagServer
			}
			if flagToken != "" {
				cfg.Token = flagToken
			}
			c := newClient(cfg)

			reachable, latency := ping(c)

			// Rich details require auth + a reachable server.
			var sys *systemInfo
			if cfg.Token != "" && reachable {
				var si systemInfo
				if err := c.get(context.Background(), "/system", &si); err == nil {
					sys = &si
				}
			}

			if jsonOutput() {
				return printJSON(map[string]any{
					"cli_version": version,
					"server":      cfg.ServerURL,
					"reachable":   reachable,
					"latency_ms":  latency.Milliseconds(),
					"logged_in":   cfg.Email,
					"system":      sys,
				})
			}

			fmt.Printf("%s %s\n", bold("autodev"), version)
			fmt.Printf("Control plane:  %s\n", cfg.ServerURL)
			if reachable {
				fmt.Printf("  status:       %s (%dms)\n", statusColor("running"), latency.Milliseconds())
			} else {
				fmt.Printf("  status:       %s\n", colorize(colorRed, "unreachable"))
			}
			if cfg.Email != "" {
				fmt.Printf("  logged in:    %s\n", cfg.Email)
			} else {
				fmt.Printf("  logged in:    %s\n", colorize(colorGray, "no — run: autodev login"))
			}

			if sys != nil {
				fmt.Printf("  version:      %s\n", sys.Version)
				fmt.Println("Subsystems:")
				for _, k := range []string{"kubernetes", "docker", "terraform", "compliance", "console"} {
					mark := colorize(colorRed, "✗")
					if sys.Subsystems[k] {
						mark = colorize(colorGreen, "✓")
					}
					fmt.Printf("  %-12s %s\n", k, mark)
				}
				if sys.IngressDomain != "" {
					fmt.Printf("Ingress domain: %s\n", sys.IngressDomain)
				}
				if sys.RegistryHost != "" {
					fmt.Printf("Registry:       %s\n", sys.RegistryHost)
				}
			} else if reachable && cfg.Token == "" {
				fmt.Println(colorize(colorGray, "  (log in to see server version + subsystems)"))
			}
			return nil
		},
	}
}

// ping checks reachability via the public /healthz endpoint and returns latency.
func ping(c *Client) (bool, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := c.req(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return false, 0
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, time.Since(start)
}
