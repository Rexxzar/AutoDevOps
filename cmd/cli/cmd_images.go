package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

type cliImage struct {
	Repo      string    `json:"repo"`
	Tag       string    `json:"tag"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	BuiltVia  string    `json:"built_via"`
}

func imagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "images",
		Aliases: []string{"registry", "image"},
		GroupID: groupPlatform,
		Short:   "Browse the local image registry",
	}
	cmd.AddCommand(imagesListCmd(), imageImportCmd(), imageDeleteCmd())
	return cmd
}

func imagesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List images in the registry",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			var imgs []cliImage
			if err := c.get(context.Background(), "/registry/images", &imgs); err != nil {
				return err
			}
			if jsonOutput() {
				return printJSON(imgs)
			}
			if len(imgs) == 0 {
				fmt.Println("No images in the registry.")
				return nil
			}
			w := tw()
			fmt.Fprintln(w, bold("REPOSITORY")+"\t"+bold("TAG")+"\t"+bold("SIZE")+"\t"+bold("BUILT VIA")+"\t"+bold("AGE"))
			for _, i := range imgs {
				age := "—"
				if !i.CreatedAt.IsZero() && i.CreatedAt.Year() > 2000 {
					age = ageStr(i.CreatedAt) + " ago"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", i.Repo, i.Tag, humanBytes(i.SizeBytes), i.BuiltVia, age)
			}
			w.Flush()
			return nil
		},
	}
}

func imageImportCmd() *cobra.Command {
	var source, repo, tag string
	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Pull an external image into the local registry",
		Args:    cobra.NoArgs,
		Example: "  autodev images import --source postgres:16",
		RunE: func(cmd *cobra.Command, args []string) error {
			if source == "" {
				return fmt.Errorf("--source is required (e.g. postgres:16)")
			}
			c, err := requireClient()
			if err != nil {
				return err
			}
			body := map[string]string{"source": source, "repo": repo, "tag": tag}
			if err := c.post(context.Background(), "/registry/images/import", body, nil); err != nil {
				return err
			}
			success(fmt.Sprintf("Imported %s", source))
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "external image ref (e.g. postgres:16)")
	cmd.Flags().StringVar(&repo, "repo", "", "target repo name (default: from source)")
	cmd.Flags().StringVar(&tag, "tag", "", "target tag (default: from source)")
	return cmd
}

func imageDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <repo> <tag>",
		Aliases: []string{"rm"},
		Short:   "Delete an image from the registry",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := requireClient()
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Delete image %s:%s?", args[0], args[1])) {
				fmt.Println("Aborted.")
				return nil
			}
			q := url.Values{"repo": {args[0]}, "tag": {args[1]}}
			if err := c.delete(context.Background(), "/registry/images?"+q.Encode()); err != nil {
				return err
			}
			success(fmt.Sprintf("Deleted %s:%s", args[0], args[1]))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGT"[exp])
}
