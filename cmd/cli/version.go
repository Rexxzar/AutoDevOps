package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build metadata, injected at build time via -ldflags:
//
//	go build -ldflags "-X main.version=1.0.0 -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// Defaults make `go run`/dev builds report "dev".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print the autodev version",
		GroupID: groupOther,
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Printf("autodev %s\n", version)
			fmt.Printf("  commit:  %s\n", commit)
			fmt.Printf("  built:   %s\n", date)
			fmt.Printf("  go:      %s (%s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
