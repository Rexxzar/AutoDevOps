package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cliResource struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Provider     string    `json:"provider"`
	Region       string    `json:"region"`
	ResourceType string    `json:"resource_type"`
	Status       string    `json:"status"`
	Outputs      string    `json:"outputs"`
	CreatedAt    time.Time `json:"created_at"`
}

func resourcesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resources",
		Aliases: []string{"res", "resource"},
		GroupID: groupResources,
		Short:   "Manage provisioned cloud & local resources",
	}
	cmd.AddCommand(
		resourcesListCmd(),
		resourceGetCmd(),
		resourceCreateCmd(),
		resourceApplyCmd(),
		resourceDestroyCmd(),
		resourceDismissCmd(),
		resourceTfCmd(),
		resourceExecCmd(),
	)
	return cmd
}

func resourcesListCmd() *cobra.Command {
	var provider, rtype, status string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List provisioned resources",
		Args:    cobra.NoArgs,
		Example: `  autodev resources list
  autodev resources list --provider aws
  autodev resources list --provider local --type docker`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var rs []cliResource
			if err := c.get(context.Background(), "/resources", &rs); err != nil {
				return err
			}
			out := []cliResource{}
			for _, r := range rs {
				if provider != "" && !strings.EqualFold(r.Provider, provider) {
					continue
				}
				if rtype != "" && !strings.EqualFold(r.ResourceType, rtype) {
					continue
				}
				if status != "" && !strings.EqualFold(r.Status, status) {
					continue
				}
				out = append(out, r)
			}
			if jsonOutput() {
				return printJSON(out)
			}
			if len(out) == 0 {
				fmt.Println("No resources found.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("NAME")+"\t"+bold("PROVIDER")+"\t"+bold("TYPE")+"\t"+bold("REGION")+"\t"+bold("STATUS")+"\t"+bold("AGE"))
			for _, r := range out {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					r.Name, strings.ToUpper(r.Provider), r.ResourceType, r.Region, statusColor(r.Status), ageStr(r.CreatedAt))
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "filter by provider: aws | azure | local")
	cmd.Flags().StringVar(&rtype, "type", "", "filter by type: ec2|rds|ecs|aci|vm|db|docker|...")
	cmd.Flags().StringVar(&status, "status", "", "filter by status: planned|applied|destroyed|failed")
	return cmd
}

func resourceGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <name|id>",
		Short:   "Show a resource's details and outputs",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev resources get my-db",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveResourceID(c, args[0])
			if err != nil {
				return err
			}
			var r cliResource
			if err := c.get(context.Background(), "/resources/"+id, &r); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(r)
			}
			fmt.Println(bold(r.Name))
			fmt.Printf("  id:        %s\n", r.ID)
			fmt.Printf("  provider:  %s\n", r.Provider)
			fmt.Printf("  type:      %s\n", r.ResourceType)
			fmt.Printf("  region:    %s\n", r.Region)
			fmt.Printf("  status:    %s\n", statusColor(r.Status))
			fmt.Printf("  age:       %s\n", ageStr(r.CreatedAt))
			if m := parseOutputs(r.Outputs); len(m) > 0 {
				fmt.Println("  outputs:")
				for _, k := range sortedKeys(m) {
					fmt.Printf("    %s = %v\n", k, m[k])
				}
			}
			return nil
		},
	}
}

func resourceApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "apply <name|id>",
		Short:   "Apply a resource (terraform apply)",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev resources apply my-db",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveResourceID(c, args[0])
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/resources/"+id+"/apply", nil, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Apply started for %s — track it with: autodev resources get %s", args[0], args[0]))
			return nil
		},
	}
}

func resourceDestroyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "destroy <name|id>",
		Short:   "Destroy a resource (terraform destroy)",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev resources destroy my-db --yes",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Destroy resource %q? This tears down the real cloud infrastructure.", args[0])) {
				fmt.Println("Aborted.")
				return nil
			}
			id, err := resolveResourceID(c, args[0])
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/resources/"+id+"/destroy", nil, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Destroy started for %s", args[0]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func resourceDismissCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dismiss <name|id>",
		Short: "Hide a destroyed resource from listings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveResourceID(c, args[0])
			if err != nil {
				return err
			}
			if err := c.post(context.Background(), "/resources/"+id+"/dismiss", nil, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Dismissed %s", args[0]))
			return nil
		},
	}
}

func resourceTfCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:     "tf <name|id>",
		Short:   "Show the generated Terraform for a resource",
		Args:    cobra.ExactArgs(1),
		Example: "  autodev resources tf my-db\n  autodev resources tf my-db --out main.tf",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveResourceID(c, args[0])
			if err != nil {
				return err
			}
			var resp struct {
				Config string `json:"config"`
			}
			if err := c.get(context.Background(), "/resources/"+id+"/terraform", &resp); err != nil {
				return err
			}
			if out != "" {
				if err := os.WriteFile(out, []byte(resp.Config), 0o644); err != nil {
					return err
				}
				success("Wrote " + out)
				return nil
			}
			fmt.Print(resp.Config)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the .tf to a file instead of stdout")
	return cmd
}

type resourceSpec struct {
	Name           string            `json:"name"`
	Provider       string            `json:"provider"`
	ResourceType   string            `json:"resource_type"`
	Region         string            `json:"region"`
	CloudAccountID string            `json:"cloud_account_id,omitempty"`
	ResourceOpts   map[string]string `json:"resource_opts,omitempty"`
}

func resourceCreateCmd() *cobra.Command {
	var file, name, provider, rtype, region, account string
	var opts []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a resource from a spec file or flags (then apply it)",
		Long: `Create a provisioned resource. Use -f for a full JSON spec, or flags for
simple cases. The resource is created + planned; run "autodev resources apply" next.`,
		Args: cobra.NoArgs,
		Example: `  autodev resources create -f resource.json
  autodev resources create --provider local --type docker --name web --opt image=nginx --opt port=80`,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var spec resourceSpec
			if file != "" {
				data, rerr := os.ReadFile(file)
				if rerr != nil {
					return rerr
				}
				if jerr := json.Unmarshal(data, &spec); jerr != nil {
					return fmt.Errorf("parse spec %s: %w", file, jerr)
				}
			} else {
				if name == "" || provider == "" || rtype == "" {
					return fmt.Errorf("provide -f <file>, or at least --name, --provider and --type")
				}
				ro, perr := parseOpts(opts)
				if perr != nil {
					return perr
				}
				spec = resourceSpec{Name: name, Provider: provider, ResourceType: rtype, Region: region, CloudAccountID: account, ResourceOpts: ro}
			}
			// Cloud resources need an account: accept a name or id for --account
			// (or the file's cloud_account_id), or fall back to the default set
			// via `autodev cloud use`.
			if !strings.EqualFold(spec.Provider, "local") {
				acct := spec.CloudAccountID
				if acct == "" {
					cfg, _ := loadConfig()
					acct = cfg.DefaultAccount
				}
				if acct == "" {
					return fmt.Errorf("cloud resources need an account — pass --account <name|id>, or set a default: autodev cloud use <name>")
				}
				id, _, rerr := resolveCloudAccount(c, acct)
				if rerr != nil {
					return rerr
				}
				spec.CloudAccountID = id
			}

			var created cliResource
			if err := c.post(context.Background(), "/resources", spec, &created); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(created)
			}
			success(fmt.Sprintf("Created %s (%s) — status %s. Apply it with: autodev resources apply %s",
				created.Name, created.ResourceType, created.Status, created.Name))
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "JSON spec file")
	cmd.Flags().StringVar(&name, "name", "", "resource name")
	cmd.Flags().StringVar(&provider, "provider", "", "aws | azure | local")
	cmd.Flags().StringVar(&rtype, "type", "", "resource type (ec2, rds, ecs, aci, vm, db, docker, ...)")
	cmd.Flags().StringVar(&region, "region", "", "region (cloud providers)")
	cmd.Flags().StringVar(&account, "account", "", "cloud account id")
	cmd.Flags().StringArrayVar(&opts, "opt", nil, "resource option key=value (repeatable)")
	return cmd
}

func resourceExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "exec <name|id> -- <command>",
		Short:   "Run a one-shot command on a resource (EC2/VM/ECS/ACI)",
		Args:    cobra.MinimumNArgs(2),
		Example: "  autodev resources exec my-vm -- uname -a",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			id, err := resolveResourceID(c, args[0])
			if err != nil {
				return err
			}
			command := strings.Join(args[1:], " ")
			var res struct {
				Stdout   string `json:"stdout"`
				Stderr   string `json:"stderr"`
				ExitCode int    `json:"exit_code"`
			}
			if err := c.post(context.Background(), "/resources/"+id+"/console/exec",
				map[string]string{"command": command}, &res); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(res)
			}
			if res.Stdout != "" {
				fmt.Print(res.Stdout)
				if !strings.HasSuffix(res.Stdout, "\n") {
					fmt.Println()
				}
			}
			if res.Stderr != "" {
				fmt.Fprint(os.Stderr, res.Stderr)
			}
			if res.ExitCode != 0 {
				os.Exit(res.ExitCode)
			}
			return nil
		},
	}
}

// --- shared helpers ---

func resolveResourceID(c *Client, arg string) (string, error) {
	if looksLikeUUID(arg) {
		return arg, nil
	}
	var rs []cliResource
	if err := c.get(context.Background(), "/resources", &rs); err != nil {
		return "", err
	}
	var matches []string
	for _, r := range rs {
		if r.Name == arg {
			matches = append(matches, r.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no resource named %q (try the id, or: autodev resources list)", arg)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple resources named %q — use the id instead", arg)
	}
}

func looksLikeUUID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}

func parseOpts(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --opt %q (want key=value)", p)
		}
		m[k] = v
	}
	return m, nil
}

func parseOutputs(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// confirm prompts for a yes/no answer on stdin (used by destructive commands).
func confirm(question string) bool {
	fmt.Printf("%s [y/N]: ", question)
	line, err := promptLine("")
	if err != nil {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}
