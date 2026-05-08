package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zuhailkhan/threadman/internal/hooks"
)

func newSetupHooksCmd() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "setup-hooks",
		Short: "Configure per-response ingestion hooks for AI providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider != "" {
				if err := hooks.Setup(provider); err != nil {
					return err
				}
				fmt.Printf("configured: %s\n", provider)
				return nil
			}
			errs := hooks.SetupAll()
			for _, e := range errs {
				fmt.Fprintln(os.Stderr, "!", e)
			}
			if len(errs) > 0 {
				return fmt.Errorf("%d provider(s) failed", len(errs))
			}
			fmt.Println("all providers configured")
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "configure a single provider (claude, gemini, opencode); default configures all")
	return cmd
}
