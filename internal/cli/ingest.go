package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zuhailkhan/threadman/internal/ports"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func newIngestCmd(svc *syncsvc.Service) *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest a session from a hook payload (reads JSON from stdin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if provider == "" {
				return fmt.Errorf("--provider is required")
			}
			var payload ports.HookPayload
			if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
				return fmt.Errorf("decode hook payload: %w", err)
			}
			ctx := context.Background()
			return svc.IngestFromHook(ctx, provider, payload)
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "provider name: claude, gemini, or opencode")
	return cmd
}
