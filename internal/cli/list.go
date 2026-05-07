package cli

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func newListCmd(svc *syncsvc.Service) *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List synced threads",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			threads, err := svc.ListThreads(ctx, provider)
			if err != nil {
				return err
			}

			home := homeDir()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "#\tPROVIDER\tTITLE\tWORKSPACE\tDATE\n")
			fmt.Fprintf(w, "-\t--------\t-----\t---------\t----\n")
			for i, t := range threads {
				title := t.Title
				if title == "" {
					title = "(untitled)"
				}
				ws := shortenHome(t.WorkspacePath, home)
				date := t.CreatedAt.Format("02 Jan 06  15:04")
				fmt.Fprintf(w, "%d\t[%s]\t%s\t%s\t%s\n", i+1, t.Provider, title, ws, date)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "filter by provider name")
	return cmd
}

func homeDir() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.HomeDir
}

func shortenHome(path, home string) string {
	if home == "" {
		return path
	}
	return strings.Replace(path, home, "~", 1)
}
