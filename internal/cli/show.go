package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zuhailkhan/threadman/internal/domain"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func newShowCmd(svc *syncsvc.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "show <number>",
		Short: "Show full conversation for a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			n, err := strconv.Atoi(args[0])
			if err != nil || n < 1 {
				return fmt.Errorf("argument must be a positive number from 'threadman list'")
			}

			threads, err := svc.ListThreads(ctx, "")
			if err != nil {
				return err
			}
			if n > len(threads) {
				return fmt.Errorf("no thread #%d (list has %d threads)", n, len(threads))
			}

			t, err := svc.GetThread(ctx, threads[n-1].ID)
			if err != nil {
				return err
			}

			var buf bytes.Buffer
			title := t.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&buf, "[%s] %s\n", t.Provider, title)
			if t.WorkspacePath != "" {
				fmt.Fprintf(&buf, "workspace: %s\n", shortenHome(t.WorkspacePath, homeDir()))
			}
			fmt.Fprintln(&buf, strings.Repeat("─", 60))

			for _, m := range t.Messages {
				writeMessage(&buf, m)
			}

			return openPager(buf.Bytes())
		},
	}
}

func writeMessage(buf *bytes.Buffer, m domain.Message) {
	switch m.Role {
	case domain.RoleUser:
		fmt.Fprintln(buf, "You:")
	case domain.RoleAssistant:
		fmt.Fprintln(buf, "Assistant:")
	default:
		fmt.Fprintf(buf, "%s:\n", m.Role)
	}
	fmt.Fprintln(buf, m.Content)
	fmt.Fprintln(buf, strings.Repeat("─", 60))
}

func openPager(content []byte) error {
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less"
	}

	pagerCmd := exec.Command(pager)
	pagerCmd.Stdin = bytes.NewReader(content)
	pagerCmd.Stdout = os.Stdout
	pagerCmd.Stderr = os.Stderr

	if err := pagerCmd.Run(); err != nil {
		// pager not available — fall back to plain print
		os.Stdout.Write(content)
	}
	return nil
}
