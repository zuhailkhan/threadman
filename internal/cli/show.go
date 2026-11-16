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
		Use:   "show <number|thread-id>",
		Short: "Show full conversation for a thread",
		Long: "Show the full conversation for a thread, chosen by its number in 'threadman list' or by its thread ID.\n" +
			"When the output is not a terminal the conversation is printed directly instead of opening a pager.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// A number is a position in 'threadman list'; anything else is a
			// thread ID, which stays valid as the list changes.
			id := args[0]
			if n, err := strconv.Atoi(args[0]); err == nil {
				if n < 1 {
					return fmt.Errorf("argument must be a positive number from 'threadman list' or a thread ID")
				}
				threads, err := svc.ListThreads(ctx, "", false)
				if err != nil {
					return err
				}
				if n > len(threads) {
					return fmt.Errorf("no thread #%d (list has %d threads)", n, len(threads))
				}
				id = threads[n-1].ID
			}

			t, err := svc.GetThread(ctx, id)
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

			if !isTerminal(os.Stdout) {
				_, err := os.Stdout.Write(buf.Bytes())
				return err
			}
			return openPager(buf.Bytes())
		},
	}
}

func writeMessage(buf *bytes.Buffer, m domain.Message) {
	switch {
	case m.Kind == domain.KindHandoff:
		// Printed in full: an agent reading this thread needs what the block
		// told the model, not only that a handoff happened.
		fmt.Fprintln(buf, "Handoff received (written by threadman):")
	case m.Role == domain.RoleUser:
		fmt.Fprintln(buf, "You:")
	case m.Role == domain.RoleAssistant:
		fmt.Fprintln(buf, "Assistant:")
	default:
		fmt.Fprintf(buf, "%s:\n", m.Role)
	}
	fmt.Fprintln(buf, m.Content)
	fmt.Fprintln(buf, strings.Repeat("─", 60))
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
