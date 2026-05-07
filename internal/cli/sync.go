package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func newSyncCmd(svc *syncsvc.Service) *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync threads from all providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var results []syncsvc.SyncResult

			if provider != "" {
				r, ok := svc.SyncProvider(ctx, provider)
				if !ok {
					return fmt.Errorf("unknown provider: %s", provider)
				}
				results = []syncsvc.SyncResult{r}
			} else {
				results = svc.SyncAll(ctx)
			}

			hasErr := false
			for _, r := range results {
				if r.Err != nil {
					fmt.Fprintf(os.Stderr, "[%s] failed: %v\n", r.Provider, r.Err)
					hasErr = true
					continue
				}

				line := fmt.Sprintf("[%-8s] %d synced", r.Provider, r.ThreadsSaved)
				if r.Skipped > 0 {
					line += fmt.Sprintf("  %d skipped", r.Skipped)
				}
				if len(r.Errors) > 0 {
					line += fmt.Sprintf("  %d errors", len(r.Errors))
				}
				fmt.Println(line)

				for _, e := range r.Errors {
					fmt.Fprintf(os.Stderr, "  ! %s\n", e)
				}
			}

			if hasErr {
				return fmt.Errorf("one or more providers failed")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "sync a single provider by name")
	return cmd
}
