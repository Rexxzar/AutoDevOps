package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

type cliCloudAccount struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func cloudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cloud",
		GroupID: groupResources,
		Short:   "Manage cloud provider accounts (AWS / Azure)",
	}
	cmd.AddCommand(cloudAccountsCmd(), cloudUseCmd(), cloudCurrentCmd())
	return cmd
}

// resolveCloudAccount resolves a name or id to (id, name) via /cloud/accounts.
func resolveCloudAccount(c *Client, arg string) (id, name string, err error) {
	var accts []cliCloudAccount
	if err := c.get(context.Background(), "/cloud/accounts", &accts); err != nil {
		return "", "", err
	}
	for _, a := range accts {
		if a.ID == arg || a.Name == arg {
			return a.ID, a.Name, nil
		}
	}
	return "", "", fmt.Errorf("no cloud account matching %q (see: autodev cloud accounts list)", arg)
}

func cloudUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "use <name|id>",
		Short:   "Set the default cloud account for resource creation",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev cloud use aws-prod-eu-west-1",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, name, err := resolveCloudAccount(c, args[0])
			if err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			cfg.DefaultAccount = id
			cfg.DefaultAccountName = name
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success(fmt.Sprintf("Default cloud account set to %s", bold(name)))
			return nil
		},
	}
}

func cloudCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the default cloud account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(map[string]string{"id": cfg.DefaultAccount, "name": cfg.DefaultAccountName})
			}
			if cfg.DefaultAccount == "" {
				fmt.Println("No default cloud account set — set one with: autodev cloud use <name>")
				return nil
			}
			fmt.Printf("%s (%s)\n", bold(cfg.DefaultAccountName), cfg.DefaultAccount)
			return nil
		},
	}
}

func cloudAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "List, add, or remove cloud accounts",
	}
	cmd.AddCommand(cloudAccountsListCmd(), cloudAccountsAddCmd(), cloudAccountsRemoveCmd())
	return cmd
}

func cloudAccountsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List registered cloud accounts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var accts []cliCloudAccount
			if err := c.get(context.Background(), "/cloud/accounts", &accts); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(accts)
			}
			if len(accts) == 0 {
				fmt.Println("No cloud accounts registered.")
				return nil
			}
			cfg, _ := loadConfig()
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("PROVIDER")+"\t"+bold("ID")+"\t"+bold("AGE"))
			for _, a := range accts {
				name := a.Name
				if a.ID == cfg.DefaultAccount {
					name += colorize(colorGreen, " *")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, a.Provider, a.ID, ageStr(a.CreatedAt))
			}
			w.Flush()
			fmt.Println(colorize(colorGray, "* default (autodev cloud use <name> to change)"))
			return nil
		},
	}
}

func cloudAccountsAddCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "add -f <file>",
		Short: "Register a cloud account from a JSON spec",
		Long: `Register a cloud account. Credentials are provider-specific and sensitive, so
they're supplied via a JSON file:

  { "provider": "aws", "name": "prod",
    "credentials": { "access_key_id": "...", "secret_access_key": "...", "region": "eu-west-2" } }`,
		Args:    cobra.NoArgs,
		Example: "  autodev cloud accounts add -f aws-account.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("-f <file> is required")
			}
			c, err := requireClient()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			var body json.RawMessage = data
			if !json.Valid(body) {
				return fmt.Errorf("%s is not valid JSON", file)
			}
			if err := c.post(context.Background(), "/cloud/accounts", body, nil); err != nil {
				return err
			}
			success("Cloud account registered.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "JSON spec file (provider, name, credentials)")
	return cmd
}

func cloudAccountsRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <name|id>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a cloud account",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, _, err := resolveCloudAccount(c, args[0])
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Remove cloud account %q?", args[0])) {
				fmt.Println("Aborted.")
				return nil
			}
			if err := c.del(context.Background(), "/cloud/accounts/"+id); err != nil {
				return err
			}
			success(fmt.Sprintf("Removed %s", args[0]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}
