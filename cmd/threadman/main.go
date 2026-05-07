package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zuhailkhan/threadman/internal/cli"
	"github.com/zuhailkhan/threadman/internal/ports"
	"github.com/zuhailkhan/threadman/internal/providers/claude"
	"github.com/zuhailkhan/threadman/internal/providers/gemini"
	"github.com/zuhailkhan/threadman/internal/providers/opencode"
	"github.com/zuhailkhan/threadman/internal/storage/sqlite"
	syncsvc "github.com/zuhailkhan/threadman/internal/sync"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	dbDir := filepath.Join(home, ".local", "share", "threadman")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	repo, err := sqlite.NewRepository(filepath.Join(dbDir, "threads.db") + "?_fk=1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening store: %v\n", err)
		os.Exit(1)
	}

	svc := syncsvc.New([]ports.ThreadProvider{
		claude.NewProvider(),
		gemini.NewProvider(),
		opencode.NewProvider(),
	}, repo)

	if err := cli.Execute(svc); err != nil {
		os.Exit(1)
	}
}
