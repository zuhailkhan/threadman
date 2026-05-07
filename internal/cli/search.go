package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func newSearchCmd(svc *syncsvc.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search threads by message content",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			threads, err := svc.SearchMessages(ctx, args[0])
			if err != nil {
				return err
			}

			for _, t := range threads {
				fmt.Printf("[%-8s] %s\n", t.Provider, t.Title)
			}
			return nil
		},
	}
}
