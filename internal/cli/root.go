package cli

import (
	"github.com/spf13/cobra"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func Execute(svc *syncsvc.Service) error {
	root := &cobra.Command{
		Use:   "threadman",
		Short: "Aggregate AI conversation threads",
	}
	root.AddCommand(newSyncCmd(svc), newListCmd(svc), newSearchCmd(svc), newShowCmd(svc))
	return root.Execute()
}
