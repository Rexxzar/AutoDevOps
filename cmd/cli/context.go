package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Global persistent flags, bound on the root command. They override the saved
// config (~/.autodev/config.json) for a single invocation — the standard
// kubectl/gh pattern (config is the default, flags win).
var (
	flagServer  string
	flagToken   string
	flagOutput  string
	flagNoColor bool
)

// Command group IDs for the grouped help screen (cobra AddGroup), so related
// commands list under headings instead of one flat block.
const (
	groupAuth      = "auth"
	groupDeploy    = "deployments"
	groupResources = "resources"
	groupPlatform  = "platform"
	groupOther     = "other"
)

// resolveClient loads the saved config, applies any --server/--token overrides,
// and returns a Client. Use for commands that don't strictly need auth (login).
func resolveClient() (*Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	if flagServer != "" {
		cfg.ServerURL = flagServer
	}
	if flagToken != "" {
		cfg.Token = flagToken
	}
	return newClient(cfg), nil
}

// requireClient is resolveClient plus an auth check — the entry point for every
// command that calls an authenticated endpoint.
func requireClient() (*Client, error) {
	c, err := resolveClient()
	if err != nil {
		return nil, err
	}
	if c.tok == "" {
		return nil, fmt.Errorf("not logged in — run: autodev login")
	}
	return c, nil
}

// jsonOutput reports whether -o/--output json was requested, so commands render
// machine-readable JSON instead of a human table.
func jsonOutput() bool { return flagOutput == "json" }

// printJSON writes v as indented JSON to stdout — the canonical scriptable output.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
