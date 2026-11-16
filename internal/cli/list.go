package cli

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zuhailkhan/threadman/internal/domain"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func newListCmd(svc *syncsvc.Service) *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List synced threads",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			threads, err := svc.ListThreads(ctx, provider, false)
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
				date := lastActivity(t).Format("02 Jan 06  15:04")
				fmt.Fprintf(w, "%d\t[%s]\t%s\t%s\t%s\n", i+1, t.Provider, title, ws, date)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "filter by provider name")
	return cmd
}

// lastActivity is the time of a thread's newest message, which is what the
// list is ordered by, or when it was created if it has none.
func lastActivity(t domain.Thread) time.Time {
	at := t.CreatedAt
	for _, m := range t.Messages {
		if m.Timestamp.After(at) {
			at = m.Timestamp
		}
	}
	return at
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
