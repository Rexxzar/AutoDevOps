package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

type cliRepo struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	RepoURL       string         `json:"repo_url"`
	Branch        string         `json:"branch"`
	LastScannedAt *time.Time     `json:"last_scanned_at"`
	Findings      map[string]int `json:"findings"`
}

func reposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "repos",
		Aliases: []string{"repo"},
		GroupID: groupPlatform,
		Short:   "Manage the Git repo store",
	}
	cmd.AddCommand(reposListCmd(), repoGetCmd(), repoAddCmd(), repoScanCmd(), repoRefreshCmd(), repoRemoveCmd())
	return cmd
}

func reposListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List stored Git repositories",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var repos []cliRepo
			if err := c.get(context.Background(), "/repos", &repos); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(repos)
			}
			if len(repos) == 0 {
				fmt.Println("No repositories.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("BRANCH")+"\t"+bold("FINDINGS")+"\t"+bold("SCANNED")+"\t"+bold("URL"))
			for _, r := range repos {
				scanned := "never"
				if r.LastScannedAt != nil {
					scanned = ageStr(*r.LastScannedAt) + " ago"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Branch, findingsSummary(r.Findings), scanned, r.RepoURL)
			}
			w.Flush()
			return nil
		},
	}
}

func repoGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name|id>",
		Short: "Show a repository's details and findings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveRepoID(c, args[0])
			if err != nil {
				return err
			}
			var r map[string]any
			if err := c.get(context.Background(), "/repos/"+id, &r); err != nil {
				return err
			}
			return printJSON(r)
		},
	}
}

func repoAddCmd() *cobra.Command {
	var name, repo, branch, gitToken string
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Register a Git repository",
		Args:    cobra.NoArgs,
		Example: "  autodev repos add --repo https://github.com/you/app --branch main",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			c, err := requireClient()
			if err != nil {
				return err
			}
			body := map[string]any{"repo_url": repo, "branch": branch}
			if name != "" {
				body["name"] = name
			}
			if gitToken != "" {
				body["git_auth"] = map[string]string{"type": "token", "token": gitToken}
			}
			if err := c.post(context.Background(), "/repos", body, nil); err != nil {
				return err
			}
			success("Repository added.")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name (default: from URL)")
	cmd.Flags().StringVar(&repo, "repo", "", "git repo URL")
	cmd.Flags().StringVar(&branch, "branch", "main", "git branch")
	cmd.Flags().StringVar(&gitToken, "git-token", "", "git token for private repos")
	return cmd
}

func repoScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan <name|id>",
		Short: "Run a compliance scan on a repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveRepoID(c, args[0])
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/repos/"+id+"/scan", nil, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Scan complete for %s — see: autodev repos get %s", args[0], args[0]))
			return nil
		},
	}
}

func repoRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <name|id>",
		Short: "Pull the latest commit for a repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveRepoID(c, args[0])
			if err != nil {
				return err
			}
			var resp map[string]any
			if err := c.post(context.Background(), "/repos/"+id+"/refresh", nil, &resp); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(resp)
			}
			success("Refreshed.")
			return nil
		},
	}
}

func repoRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <name|id>",
		Aliases: []string{"rm"},
		Short:   "Remove a repository from the store",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveRepoID(c, args[0])
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Remove repository %q from the store?", args[0])) {
				fmt.Println("Aborted.")
				return nil
			}
			if err := c.delete(context.Background(), "/repos/"+id); err != nil {
				return err
			}
			success(fmt.Sprintf("Removed %s", args[0]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func resolveRepoID(c *Client, arg string) (string, error) {
	if looksLikeUUID(arg) {
		return arg, nil
	}
	var repos []cliRepo
	if err := c.get(context.Background(), "/repos", &repos); err != nil {
		return "", err
	}
	for _, r := range repos {
		if r.Name == arg {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no repository named %q (see: autodev repos list)", arg)
}

func findingsSummary(f map[string]int) string {
	c, h := f["critical"], f["high"]
	if c == 0 && h == 0 {
		total := 0
		for _, v := range f {
			total += v
		}
		if total == 0 {
			return colorize(colorGreen, "clean")
		}
		return fmt.Sprintf("%d", total)
	}
	return fmt.Sprintf("%s %s", colorize(colorRed, fmt.Sprintf("%dC", c)), colorize(colorYellow, fmt.Sprintf("%dH", h)))
}
