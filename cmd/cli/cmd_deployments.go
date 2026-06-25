package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// apiDeployment is the list-row shape from GET /deployments.
type apiDeployment struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RepoURL    string    `json:"repo_url"`
	Branch     string    `json:"branch"`
	Framework  string    `json:"framework"`
	TargetType string    `json:"target_type"`
	Status     string    `json:"status"`
	ImageRef   string    `json:"image_ref"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type cliStack struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Apps []cliApp `json:"apps"`
}

type cliApp struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	TargetType string    `json:"target_type"`
	Status     string    `json:"status"`
	RepoURL    string    `json:"repo_url"`
	Branch     string    `json:"branch"`
	Framework  string    `json:"framework"`
	ImageRef   string    `json:"image_ref"`
	Domain     string    `json:"domain"`
	HostPort   uint32    `json:"host_port"`
	Links      []cliLink `json:"links"`
	CreatedAt  time.Time `json:"created_at"`
}

type cliLink struct {
	ID                    string `json:"id"`
	Alias                 string `json:"alias"`
	DependsOnAppName      string `json:"depends_on_app_name"`
	DependsOnResourceName string `json:"depends_on_resource_name"`
	Port                  int    `json:"port"`
}

func deploymentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deployments",
		Aliases: []string{"deploy", "deps", "deployment"},
		GroupID: groupDeploy,
		Short:   "Create and manage deployments (apps, links, logs)",
	}
	cmd.AddCommand(
		deploymentsListCmd(),
		deploymentGetCmd(),
		deploymentCreateCmd(),
		deploymentTriggerCmd(),
		deploymentDestroyCmd(),
		deploymentLogsCmd(),
		deploymentAppsCmd(),
		deploymentLinkCmd(),
		deploymentLinksCmd(),
		deploymentUnlinkCmd(),
		deploymentImportCmd(),
	)
	return cmd
}

func deploymentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List deployments",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var deps []apiDeployment
			if err := c.get(context.Background(), "/deployments", &deps); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(deps)
			}
			if len(deps) == 0 {
				fmt.Println("No deployments found.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("TARGET")+"\t"+bold("STATUS")+"\t"+bold("FRAMEWORK")+"\t"+bold("AGE"))
			for _, d := range deps {
				fw := d.Framework
				if fw == "" {
					fw = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Name, d.TargetType, statusColor(d.Status), fw, ageStr(d.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
}

func deploymentGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <name>",
		Short:   "Show a deployment's apps, status, and links",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev deployments get shophub",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(stack)
			}
			fmt.Printf("%s  (%d app%s)\n", bold(stack.Name), len(stack.Apps), plural(len(stack.Apps)))
			w := tw()
			fmt.Fprintln(w, "  "+bold("APP")+"\t"+bold("KIND")+"\t"+bold("TARGET")+"\t"+bold("STATUS")+"\t"+bold("DOMAIN")+"\t"+bold("AGE"))
			for _, a := range stack.Apps {
				domain := a.Domain
				if domain == "" {
					domain = "—"
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", a.Name, a.Kind, a.TargetType, statusColor(a.Status), domain, ageStr(a.CreatedAt))
			}
			w.Flush()
			for _, a := range stack.Apps {
				for _, l := range a.Links {
					tgt := l.DependsOnAppName
					if tgt == "" {
						tgt = l.DependsOnResourceName
					}
					fmt.Printf("  link: %s → %s (%s)\n", a.Name, tgt, l.Alias)
				}
			}
			return nil
		},
	}
}

func deploymentCreateCmd() *cobra.Command {
	var f deployFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a deployment (stack + first app)",
		Args:  cobra.NoArgs,
		Example: `  autodev deployments create --name shophub --target k8s --repo https://github.com/you/app
  autodev deployments create --name cache --target container --kind image --image redis:7 --port 6379
  autodev deployments create --name api --target ecs --resource my-ecs --repo https://github.com/you/api
  autodev deployments create -f deployment.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			spec, err := f.spec(c)
			if err != nil {
				return err
			}
			var resp createDeployResp
			if err := c.post(context.Background(), "/deployments", spec, &resp); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(resp)
			}
			success(fmt.Sprintf("Created deployment %s", bold(spec.Name)))
			if spec.Kind != "image" && resp.WebhookURL != "" {
				fmt.Printf("  webhook url:    %s\n", resp.WebhookURL)
				fmt.Printf("  webhook secret: %s\n", resp.WebhookSecret)
			}
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func deploymentTriggerCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "trigger <name> [app]",
		Aliases: []string{"redeploy"},
		Short:   "Redeploy an app (rebuilds + redeploys)",
		Args:    cobra.RangeArgs(1, 2),
		Example: "  autodev deployments trigger shophub\n  autodev deployments trigger shophub frontend",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			appArg := ""
			if len(args) == 2 {
				appArg = args[1]
			}
			appID, err := resolveAppID(stack, appArg)
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/deployments/"+stack.ID+"/apps/"+appID+"/trigger", nil, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Redeploy triggered — follow it with: autodev deployments logs %s", args[0]))
			return nil
		},
	}
}

func deploymentDestroyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy <name>",
		Short: "Destroy a deployment and all its apps",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Destroy deployment %q and all its apps?", args[0])) {
				fmt.Println("Aborted.")
				return nil
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			if err := c.delete(context.Background(), "/deployments/"+stack.ID); err != nil {
				return err
			}
			success(fmt.Sprintf("Destroyed %s", args[0]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func deploymentLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name>",
		Short: "Stream live deploy + health events for a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			appIDs := map[string]bool{}
			for _, a := range stack.Apps {
				appIDs[a.ID] = true
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			fmt.Printf("%s Streaming events for %s (Ctrl+C to stop)\n\n", colorize(colorCyan, "→"), bold(stack.Name))

			return c.streamSSE(ctx, "/events", func(ev SSEEvent) {
				var p map[string]any
				if json.Unmarshal([]byte(ev.Data), &p) != nil {
					return
				}
				if !eventForStack(p, stack.ID, appIDs) {
					return
				}
				ts := colorize(colorGray, time.Now().Format("15:04:05"))
				switch ev.Type {
				case "deploy.status":
					fmt.Printf("%s  status → %s\n", ts, statusColor(str(p["status"])))
				case "deploy.step":
					fmt.Printf("%s  %s: %s\n", ts, str(p["step"]), statusColor(str(p["status"])))
				case "deploy.log":
					fmt.Printf("%s  %s\n", ts, str(p["line"]))
				case "compliance.finding":
					fmt.Printf("%s  %s [%s] %s\n", ts, severityColor(str(p["severity"])), str(p["rule_id"]), str(p["message"]))
				case "container.health":
					fmt.Printf("%s  container → %s\n", ts, statusColor(str(p["status"])))
				}
			})
		},
	}
}

func deploymentAppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Manage apps within a deployment",
	}
	cmd.AddCommand(deploymentAppsListCmd(), deploymentAppsAddCmd(), deploymentAppsRemoveCmd())
	return cmd
}

func deploymentAppsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <name>",
		Aliases: []string{"ls"},
		Short:   "List apps in a deployment",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(stack.Apps)
			}
			w := tw()
			fmt.Fprintln(w, bold("APP")+"\t"+bold("KIND")+"\t"+bold("TARGET")+"\t"+bold("STATUS")+"\t"+bold("AGE"))
			for _, a := range stack.Apps {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", a.Name, a.Kind, a.TargetType, statusColor(a.Status), ageStr(a.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
}

func deploymentAppsAddCmd() *cobra.Command {
	var f deployFlags
	cmd := &cobra.Command{
		Use:     "add <deployment>",
		Short:   "Add an app to an existing deployment",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev deployments apps add shophub --name worker --target container --repo https://github.com/you/worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			spec, err := f.spec(c)
			if err != nil {
				return err
			}
			var resp createDeployResp
			if err := c.post(context.Background(), "/deployments/"+stack.ID+"/apps", spec, &resp); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(resp)
			}
			success(fmt.Sprintf("Added app %s to %s", bold(spec.Name), args[0]))
			if spec.Kind != "image" && resp.WebhookURL != "" {
				fmt.Printf("  webhook url:    %s\n  webhook secret: %s\n", resp.WebhookURL, resp.WebhookSecret)
			}
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func deploymentAppsRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "remove <deployment> <app>",
		Aliases: []string{"rm"},
		Short:   "Remove an app from a deployment",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			appID, err := resolveAppID(stack, args[1])
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Remove app %q from %q?", args[1], args[0])) {
				fmt.Println("Aborted.")
				return nil
			}
			if err := c.delete(context.Background(), "/deployments/"+stack.ID+"/apps/"+appID); err != nil {
				return err
			}
			success(fmt.Sprintf("Removed %s", args[1]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func deploymentLinkCmd() *cobra.Command {
	var alias string
	var port int
	cmd := &cobra.Command{
		Use:     "link <deployment> <app> <target-app>",
		Short:   "Link one app to another (injects AUTODEVOPS_<ALIAS>_* env)",
		Args:    cobra.ExactArgs(3),
		Example: "  autodev deployments link shophub backend db --alias DB --port 5432",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			appID, err := resolveAppID(stack, args[1])
			if err != nil {
				return err
			}
			targetID, err := resolveAppID(stack, args[2])
			if err != nil {
				return err
			}
			if alias == "" {
				alias = args[2]
			}
			body := map[string]any{"alias": alias, "depends_on_app_id": targetID, "port": port}
			if err := c.post(context.Background(), "/deployments/"+stack.ID+"/apps/"+appID+"/links", body, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Linked %s → %s (alias %s)", args[1], args[2], alias))
			return nil
		},
	}
	cmd.Flags().StringVar(&alias, "alias", "", "env alias (default: target app name)")
	cmd.Flags().IntVar(&port, "port", 0, "target port")
	return cmd
}

func deploymentLinksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "links <deployment>",
		Short: "List app links in a deployment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			w := tw()
			fmt.Fprintln(w, bold("APP")+"\t"+bold("→ TARGET")+"\t"+bold("ALIAS")+"\t"+bold("PORT"))
			any := false
			for _, a := range stack.Apps {
				for _, l := range a.Links {
					tgt := l.DependsOnAppName
					if tgt == "" {
						tgt = l.DependsOnResourceName
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", a.Name, tgt, l.Alias, l.Port)
					any = true
				}
			}
			w.Flush()
			if !any {
				fmt.Println("No links.")
			}
			return nil
		},
	}
}

func deploymentUnlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink <deployment> <app> <alias>",
		Short: "Remove an app link",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			stack, err := resolveStack(c, args[0])
			if err != nil {
				return err
			}
			var appID, linkID string
			for _, a := range stack.Apps {
				if a.Name == args[1] || a.ID == args[1] {
					appID = a.ID
					for _, l := range a.Links {
						if l.Alias == args[2] {
							linkID = l.ID
						}
					}
				}
			}
			if appID == "" {
				return fmt.Errorf("app %q not found", args[1])
			}
			if linkID == "" {
				return fmt.Errorf("no link with alias %q on %q", args[2], args[1])
			}
			if err := c.delete(context.Background(), "/deployments/"+stack.ID+"/apps/"+appID+"/links/"+linkID); err != nil {
				return err
			}
			success("Unlinked.")
			return nil
		},
	}
}

// --- compose import ---

type cliComposePlan struct {
	Services []cliComposeService `json:"services"`
	Links    []cliComposeLink    `json:"links"`
	Warnings []string            `json:"warnings"`
}

type cliComposeService struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"` // "git" | "image" | "" (skipped)
	Image        string            `json:"image"`
	BuildContext string            `json:"build_context"`
	Port         int               `json:"port"`
	Env          map[string]string `json:"env"`
	VolumePath   string            `json:"volume_path"`
	Warnings     []string          `json:"warnings"`
}

type cliComposeLink struct {
	AppName       string `json:"app_name"`
	DependsOnName string `json:"depends_on_name"`
	Alias         string `json:"alias"`
}

func deploymentImportCmd() *cobra.Command {
	var repo, branch, gitToken, target string
	var yes bool
	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Import a docker-compose repo as a multi-app deployment",
		Args:    cobra.NoArgs,
		Example: "  autodev deployments import --repo https://github.com/you/app --target container",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			if repo == "" || target == "" {
				return fmt.Errorf("--repo and --target are required")
			}
			var ga *gitAuthSpec
			previewBody := map[string]any{"repo_url": repo, "branch": branch}
			if gitToken != "" {
				ga = &gitAuthSpec{Type: "token", Token: gitToken}
				previewBody["git_auth"] = ga
			}

			var plan cliComposePlan
			if err := c.post(context.Background(), "/deployments/import/preview", previewBody, &plan); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(plan)
			}

			// Show the parsed plan.
			fmt.Println(bold("Services parsed from docker-compose:"))
			w := tw()
			fmt.Fprintln(w, "  "+bold("NAME")+"\t"+bold("KIND")+"\t"+bold("DETAIL"))
			included := []cliComposeService{}
			for _, s := range plan.Services {
				detail, kind := "", s.Kind
				switch s.Kind {
				case "git":
					detail = "build from source"
					if s.BuildContext != "" {
						detail += " (" + s.BuildContext + ")"
					}
					included = append(included, s)
				case "image":
					detail = s.Image
					included = append(included, s)
				default:
					kind, detail = colorize(colorGray, "skipped"), colorize(colorGray, "unsupported")
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", s.Name, kind, detail)
			}
			w.Flush()
			for _, l := range plan.Links {
				fmt.Printf("  link: %s → %s (%s)\n", l.AppName, l.DependsOnName, l.Alias)
			}
			for _, warn := range plan.Warnings {
				fmt.Println(colorize(colorYellow, "  ! "+warn))
			}
			if len(included) == 0 {
				return fmt.Errorf("no importable services found")
			}

			if !yes && !confirm(fmt.Sprintf("Create a %q deployment from these %d service(s)?", target, len(included))) {
				fmt.Println("Aborted.")
				return nil
			}

			// First git service anchors the stack; others are added as apps.
			firstGit := -1
			for i, s := range included {
				if s.Kind == "git" {
					firstGit = i
					break
				}
			}
			if firstGit == -1 {
				return fmt.Errorf("compose import needs at least one buildable (git/build) service to anchor the deployment")
			}

			var stack createDeployResp
			if err := c.post(context.Background(), "/deployments", composeSpec(included[firstGit], target, ga, repo, branch), &stack); err != nil {
				return fmt.Errorf("create stack from %s: %w", included[firstGit].Name, err)
			}
			success(fmt.Sprintf("Created deployment %s (from %s)", bold(included[firstGit].Name), included[firstGit].Name))

			for i, s := range included {
				if i == firstGit {
					continue
				}
				if err := c.post(context.Background(), "/deployments/"+stack.ID+"/apps", composeSpec(s, target, ga, repo, branch), nil); err != nil {
					fmt.Println(colorize(colorRed, "  ✗ add "+s.Name+": "+err.Error()))
					continue
				}
				fmt.Printf("  + added %s\n", s.Name)
			}

			// Create suggested links (resolve names → app ids).
			if len(plan.Links) > 0 {
				full, err := resolveStack(c, stack.ID)
				if err == nil {
					byName := map[string]string{}
					for _, a := range full.Apps {
						byName[a.Name] = a.ID
					}
					for _, l := range plan.Links {
						src, ok1 := byName[l.AppName]
						dst, ok2 := byName[l.DependsOnName]
						if !ok1 || !ok2 {
							continue
						}
						body := map[string]any{"alias": l.Alias, "depends_on_app_id": dst}
						if err := c.post(context.Background(), "/deployments/"+stack.ID+"/apps/"+src+"/links", body, nil); err == nil {
							fmt.Printf("  ↔ linked %s → %s\n", l.AppName, l.DependsOnName)
						}
					}
				}
			}
			success("Import complete — track it with: autodev deployments get " + included[firstGit].Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "git repo URL containing docker-compose.yml (required)")
	cmd.Flags().StringVar(&branch, "branch", "main", "git branch")
	cmd.Flags().StringVar(&gitToken, "git-token", "", "git token for private repos")
	cmd.Flags().StringVar(&target, "target", "", "k8s | container (required)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func composeSpec(s cliComposeService, target string, ga *gitAuthSpec, repo, branch string) deploySpec {
	spec := deploySpec{Name: s.Name, TargetType: target}
	if s.Kind == "image" {
		spec.Kind = "image"
		spec.ImageRef = s.Image
		spec.Port = s.Port
		spec.Env = s.Env
		spec.VolumePath = s.VolumePath
	} else {
		spec.Kind = "git"
		spec.RepoURL = repo
		spec.Branch = branch
		spec.BuildContext = s.BuildContext
		spec.GitAuth = ga
	}
	return spec
}

// --- shared spec + helpers ---

type createDeployResp struct {
	ID            string `json:"id"`
	WebhookURL    string `json:"webhook_url"`
	WebhookSecret string `json:"webhook_secret"`
}

type gitAuthSpec struct {
	Type   string `json:"type"`
	Token  string `json:"token,omitempty"`
	SSHKey string `json:"ssh_key,omitempty"`
}

type deploySpec struct {
	Name              string            `json:"name"`
	Kind              string            `json:"kind,omitempty"`
	RepoURL           string            `json:"repo_url,omitempty"`
	Branch            string            `json:"branch,omitempty"`
	TargetType        string            `json:"target_type"`
	TargetResourceID  string            `json:"target_resource_id,omitempty"`
	Domain            string            `json:"domain,omitempty"`
	GitAuth           *gitAuthSpec      `json:"git_auth,omitempty"`
	BuildContext      string            `json:"build_context,omitempty"`
	DeclaredFramework string            `json:"declared_framework,omitempty"`
	ImageRef          string            `json:"image_ref,omitempty"`
	Port              int               `json:"port,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	VolumePath        string            `json:"volume_path,omitempty"`
}

type deployFlags struct {
	file, name, target, resource, kind, repo, branch, gitToken, buildContext, framework, image, domain, volume string
	port                                                                                                       int
	env                                                                                                        []string
}

func (f *deployFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVarP(&f.file, "file", "f", "", "JSON spec file (full control)")
	fl.StringVar(&f.name, "name", "", "app / deployment name")
	fl.StringVar(&f.target, "target", "", "k8s | container | vm | ecs | aci")
	fl.StringVar(&f.resource, "resource", "", "provisioned resource (name|id) for cloud targets")
	fl.StringVar(&f.kind, "kind", "git", "git | image")
	fl.StringVar(&f.repo, "repo", "", "git repo URL (kind=git)")
	fl.StringVar(&f.branch, "branch", "main", "git branch")
	fl.StringVar(&f.gitToken, "git-token", "", "git token for private repos")
	fl.StringVar(&f.buildContext, "build-context", "", "subdirectory to build from")
	fl.StringVar(&f.framework, "framework", "", "declared framework hint")
	fl.StringVar(&f.image, "image", "", "image ref (kind=image)")
	fl.IntVar(&f.port, "port", 0, "container port (kind=image)")
	fl.StringArrayVar(&f.env, "env", nil, "env key=value (kind=image, repeatable)")
	fl.StringVar(&f.volume, "volume", "", "volume mount path (kind=image)")
	fl.StringVar(&f.domain, "domain", "", "custom domain")
}

func (f *deployFlags) spec(c *Client) (deploySpec, error) {
	if f.file != "" {
		data, err := os.ReadFile(f.file)
		if err != nil {
			return deploySpec{}, err
		}
		var s deploySpec
		if err := json.Unmarshal(data, &s); err != nil {
			return deploySpec{}, fmt.Errorf("parse %s: %w", f.file, err)
		}
		return s, nil
	}
	if f.name == "" || f.target == "" {
		return deploySpec{}, fmt.Errorf("--name and --target are required (or use -f <file>)")
	}
	kind := f.kind
	if kind == "" {
		kind = "git"
	}
	s := deploySpec{Name: f.name, Kind: kind, TargetType: f.target, Domain: f.domain}
	rid, err := resolveTargetResource(c, f.target, f.resource)
	if err != nil {
		return s, err
	}
	s.TargetResourceID = rid

	if kind == "image" {
		if f.image == "" {
			return s, fmt.Errorf("--image is required for kind=image")
		}
		env, perr := parseOpts(f.env)
		if perr != nil {
			return s, perr
		}
		s.ImageRef, s.Port, s.VolumePath, s.Env = f.image, f.port, f.volume, env
	} else {
		if f.repo == "" {
			return s, fmt.Errorf("--repo is required for kind=git")
		}
		s.RepoURL, s.Branch, s.BuildContext, s.DeclaredFramework = f.repo, f.branch, f.buildContext, f.framework
		if f.gitToken != "" {
			s.GitAuth = &gitAuthSpec{Type: "token", Token: f.gitToken}
		}
	}
	return s, nil
}

func resolveTargetResource(c *Client, target, resource string) (string, error) {
	switch target {
	case "vm", "ecs", "aci":
		if resource == "" {
			return "", fmt.Errorf("--resource <name|id> is required for cloud target %q", target)
		}
		return resolveResourceID(c, resource)
	}
	return "", nil
}

func resolveStack(c *Client, name string) (cliStack, error) {
	var deps []apiDeployment
	if err := c.get(context.Background(), "/deployments", &deps); err != nil {
		return cliStack{}, err
	}
	id := ""
	for _, d := range deps {
		if d.Name == name || d.ID == name {
			id = d.ID
			break
		}
	}
	if id == "" {
		if looksLikeUUID(name) {
			id = name
		} else {
			return cliStack{}, fmt.Errorf("deployment %q not found (see: autodev deployments list)", name)
		}
	}
	var s cliStack
	if err := c.get(context.Background(), "/deployments/"+id, &s); err != nil {
		return cliStack{}, err
	}
	return s, nil
}

func resolveAppID(stack cliStack, name string) (string, error) {
	if name == "" {
		if len(stack.Apps) == 1 {
			return stack.Apps[0].ID, nil
		}
		return "", fmt.Errorf("deployment has %d apps — specify which one", len(stack.Apps))
	}
	for _, a := range stack.Apps {
		if a.Name == name || a.ID == name {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("app %q not found in deployment %q", name, stack.Name)
}

func eventForStack(p map[string]any, stackID string, appIDs map[string]bool) bool {
	if str(p["deployment_id"]) == stackID || str(p["id"]) == stackID {
		return true
	}
	if id := str(p["deployment_app_id"]); id != "" && appIDs[id] {
		return true
	}
	if id := str(p["app_id"]); id != "" && appIDs[id] {
		return true
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
