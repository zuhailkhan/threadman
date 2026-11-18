package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zuhailkhan/threadman/internal/cli"
	"github.com/zuhailkhan/threadman/internal/handoff"
	"github.com/zuhailkhan/threadman/internal/llm"
	"github.com/zuhailkhan/threadman/internal/ports"
	"github.com/zuhailkhan/threadman/internal/providers/antigravity"
	"github.com/zuhailkhan/threadman/internal/providers/claude"
	"github.com/zuhailkhan/threadman/internal/providers/codex"
	"github.com/zuhailkhan/threadman/internal/providers/opencode"
	"github.com/zuhailkhan/threadman/internal/providers/pi"
	"github.com/zuhailkhan/threadman/internal/storage/sqlite"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
	"github.com/zuhailkhan/threadman/internal/tui"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(home, ".local", "share", "threadman", "threads.db")
	if err := sqlite.PrepareDatabaseFile(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "error preparing store: %v\n", err)
		os.Exit(1)
	}

	// Hooks and the TUI can write at the same moment; the busy timeout makes
	// one wait for the other instead of failing with "database is locked".
	repo, err := sqlite.NewRepository(dbPath + "?_fk=1&_busy_timeout=5000")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening store: %v\n", err)
		os.Exit(1)
	}

	svc := syncsvc.New([]ports.ThreadProvider{
		claude.NewProvider(),
		opencode.NewProvider(),
		pi.NewProvider(),
		antigravity.NewProvider(),
		codex.NewProvider(),
	}, repo)

	writers := []ports.ThreadWriter{
		claude.NewWriter(),
		opencode.NewWriter(),
		pi.NewWriter(),
		antigravity.NewWriter(),
		codex.NewWriter(),
	}

	cfgPath, err := llm.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := cli.Execute(svc, cfgPath, func() {
		m := tui.New(svc, dbPath, writers, handoff.NewSummarizer(cfgPath))
		p := tea.NewProgram(m, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}); err != nil {
		os.Exit(1)
	}
}
