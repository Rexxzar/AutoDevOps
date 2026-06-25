package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func loginCmd() *cobra.Command {
	var email, password string
	var passwordStdin bool

	cmd := &cobra.Command{
		Use:     "login",
		GroupID: groupAuth,
		Short:   "Authenticate with the AutoDevOps control plane",
		Long: `Authenticate with email + password and save a token to ~/.autodev/config.json.

The password can be a flag, entered interactively (hidden), or piped via
--password-stdin (keeps it out of shell history — the docker/gh pattern). Use the
global --server flag to point at a specific control plane.`,
		Example: `  autodev login --email admin@autodevops.local
  autodev login --server http://localhost:8080 --email admin@autodevops.local
  echo "$PASS" | autodev login --email admin@autodevops.local --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if flagServer != "" {
				cfg.ServerURL = flagServer
			}

			if email == "" {
				if email, err = promptLine("Email: "); err != nil {
					return err
				}
			}
			email = strings.TrimSpace(email)
			if email == "" {
				return fmt.Errorf("email is required")
			}

			switch {
			case passwordStdin:
				b, rerr := io.ReadAll(os.Stdin)
				if rerr != nil {
					return fmt.Errorf("read password from stdin: %w", rerr)
				}
				password = strings.TrimRight(string(b), "\r\n")
			case password == "":
				if password, err = promptPassword("Password: "); err != nil {
					return err
				}
			}
			if password == "" {
				return fmt.Errorf("password is required")
			}

			c := newClient(cfg)
			var resp struct {
				Token string `json:"token"`
			}
			if err := c.post(context.Background(), "/auth/login", map[string]string{
				"email":    email,
				"password": password,
			}, &resp); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}

			cfg.Token = resp.Token
			cfg.Email = email
			if err := saveConfig(cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			if jsonOutput() {
				return printJSON(map[string]string{"server": cfg.ServerURL, "email": email})
			}
			success(fmt.Sprintf("Logged in as %s → %s", bold(email), cfg.ServerURL))
			return nil
		},
	}

	cmd.Flags().StringVar(&email, "email", "", "Email address (prompted if omitted)")
	cmd.Flags().StringVar(&password, "password", "", "Password (prompted, hidden, if omitted)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin")
	return cmd
}

// promptLine reads one line from stdin with a visible prompt.
func promptLine(label string) (string, error) {
	fmt.Print(label)
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// promptPassword reads a password from the terminal without echoing it.
func promptPassword(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}
