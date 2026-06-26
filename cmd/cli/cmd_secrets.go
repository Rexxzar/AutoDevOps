package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cliSecret struct {
	Name       string    `json:"name"`
	Category   string    `json:"category"`
	ScopeLabel string    `json:"scope_label"`
	Managed    bool      `json:"managed"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "secrets",
		Aliases: []string{"secret"},
		GroupID: groupPlatform,
		Short:   "Manage the AES-256-GCM secrets vault",
	}
	cmd.AddCommand(secretsListCmd(), secretGetCmd(), secretSetCmd(), secretDeleteCmd(),
		secretsStatusCmd(), secretsRotateCmd(), secretsRotateMasterCmd())
	return cmd
}

func secretsListCmd() *cobra.Command {
	var category string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List vault secrets (metadata only — values never shown here)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var secrets []cliSecret
			if err := c.get(context.Background(), "/secrets", &secrets); err != nil {
				return err
			}
			out := []cliSecret{}
			for _, s := range secrets {
				if category != "" && !strings.EqualFold(s.Category, category) {
					continue
				}
				out = append(out, s)
			}
			if jsonOutput() {
				return printJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No secrets.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("CATEGORY")+"\t"+bold("SCOPE")+"\t"+bold("UPDATED"))
			for _, s := range out {
				scope := s.ScopeLabel
				if scope == "" {
					scope = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Category, scope, ageStr(s.UpdatedAt)+" ago")
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "filter: webhook|git-credentials|image-env|secret-files|cloud-account|git-repo|user")
	return cmd
}

func secretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <name>",
		Short:   "Reveal a secret's value",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev secrets get my-api-key",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var resp struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}
			if err := c.get(context.Background(), "/secrets/"+url.PathEscape(args[0]), &resp); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(resp)
			}
			fmt.Println(resp.Value)
			return nil
		},
	}
}

func secretSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <name> [value]",
		Short:   "Create or update a user secret (value via arg or stdin)",
		Args:    cobra.RangeArgs(1, 2),
		Example: "  autodev secrets set my-api-key s3cr3t\n  echo -n s3cr3t | autodev secrets set my-api-key",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var value string
			if len(args) == 2 {
				value = args[1]
			} else {
				b, rerr := io.ReadAll(bufio.NewReader(os.Stdin))
				if rerr != nil {
					return rerr
				}
				value = strings.TrimRight(string(b), "\r\n")
			}
			if value == "" {
				return fmt.Errorf("value is required (arg or stdin)")
			}
			if err := c.post(context.Background(), "/secrets", map[string]string{"name": args[0], "value": value}, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Stored secret %s", bold(args[0])))
			return nil
		},
	}
}

func secretDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a secret",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete secret %q? (breaks anything relying on it)", args[0])) {
				fmt.Println("Aborted.")
				return nil
			}
			if err := c.delete(context.Background(), "/secrets/"+url.PathEscape(args[0])); err != nil {
				return err
			}
			success(fmt.Sprintf("Deleted %s", args[0]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func secretsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show vault key status (version, rotations, secret count)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var st struct {
				Version       int `json:"version"`
				RotationCount int `json:"rotation_count"`
				SecretCount   int `json:"secret_count"`
			}
			if err := c.get(context.Background(), "/secrets/status", &st); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(st)
			}
			fmt.Printf("%s\n", bold("Secrets vault (envelope encryption)"))
			fmt.Printf("  data key version: v%d\n", st.Version)
			fmt.Printf("  rotations:        %d\n", st.RotationCount-1)
			fmt.Printf("  secrets stored:   %d\n", st.SecretCount)
			return nil
		},
	}
}

func secretsRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Rotate the data key (re-encrypts all secrets; values unchanged)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var resp struct {
				Version     int `json:"version"`
				Reencrypted int `json:"reencrypted"`
			}
			if err := c.post(context.Background(), "/secrets/rotate", nil, &resp); err != nil {
				return err
			}
			success(fmt.Sprintf("Rotated to data key v%d — %d secret(s) re-encrypted", resp.Version, resp.Reencrypted))
			return nil
		},
	}
}

func secretsRotateMasterCmd() *cobra.Command {
	var newKey string
	cmd := &cobra.Command{
		Use:   "rotate-master",
		Short: "Re-wrap the data key under a new MASTER_KEY (update env before restart!)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if newKey == "" {
				return fmt.Errorf("--new-key is required")
			}
			c, err := requireClient()
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/secrets/rotate-master", map[string]string{"new_master_key": newKey}, nil); err != nil {
				return err
			}
			success("Master key rotated.")
			fmt.Println(colorize(colorYellow, "  ! Update the MASTER_KEY env var to the new value before the next restart, or the vault won't start."))
			return nil
		},
	}
	cmd.Flags().StringVar(&newKey, "new-key", "", "the new master key")
	return cmd
}
