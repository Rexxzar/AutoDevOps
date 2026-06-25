package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		GroupID: groupAuth,
		Short:   "Remove saved credentials",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if cfg.Token == "" {
				fmt.Println("Already logged out.")
				return nil
			}
			cfg.Token = ""
			cfg.Email = ""
			if err := saveConfig(cfg); err != nil {
				return err
			}
			success("Logged out.")
			return nil
		},
	}
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "whoami",
		GroupID: groupAuth,
		Short:   "Show the current login (server + user)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if flagServer != "" {
				cfg.ServerURL = flagServer
			}
			if cfg.Token == "" {
				return fmt.Errorf("not logged in — run: autodev login")
			}
			if jsonOutput() {
				return printJSON(map[string]string{"server": cfg.ServerURL, "email": cfg.Email})
			}
			email := cfg.Email
			if email == "" {
				email = "(unknown)"
			}
			fmt.Printf("%s @ %s\n", bold(email), cfg.ServerURL)
			return nil
		},
	}
}
