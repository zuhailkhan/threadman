# threadman

`threadman` is a terminal interface for managing AI conversation threads across Claude and OpenCode — browse, search, and hand off a conversation from one provider to another.

Everything runs locally: threadman reads the session files your CLIs already write, indexes them into a SQLite database at `~/.local/share/threadman/threads.db`, and makes no network calls.

## Features

- **Interactive TUI** — full-screen thread browser with colors, navigation, and inline search
- **Multi-provider import** — reads local session files from Claude Code and OpenCode; first run lets you pick which providers to import
- **Live updates** — optional per-response hooks ingest new messages as you chat, and the TUI refreshes automatically
- **Thread reader** — scrollable message view with You/Assistant labels
- **Handoff** — transfer any conversation to a different provider's native session format and open it immediately
- **Thread grouping** — threads handed off across providers are grouped into a single row with provider icon clusters `[C O]`
- **CLI subcommands** — `sync`, `list`, `search`, `show` for scripting


<img width="1920" height="1045" alt="image" src="https://github.com/user-attachments/assets/94d4fbc4-be9c-40f6-bb5a-56d487347d17" />


## Supported Providers

| Provider | Source |
|----------|--------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenCode (1.x and 2.x) | `~/.local/share/opencode/opencode.db` |

> **Gemini CLI support has been removed.** Google replaced Gemini CLI with Antigravity CLI, so threadman no longer syncs from or hands off to it. Gemini threads already in your local database stay listed and readable.

## Usage

```bash
# Launch interactive TUI
threadman

# Sync all providers to local database (--provider claude|opencode for one)
threadman sync

# List threads (--provider to filter)
threadman list

# Search threads
threadman search <query>

# Print a thread by its number in `threadman list`
threadman show <number>

# Install per-response ingestion hooks (--provider to configure just one)
threadman setup-hooks
```

`setup-hooks` adds a `Stop` hook to `~/.claude/settings.json` and an OpenCode plugin at `~/.config/opencode/plugins/threadman.ts`. Both call `threadman ingest`, which reads a hook payload from stdin; you normally don't run it yourself.

### TUI Keybindings

| Key | Action |
|-----|--------|
| ↑ / k | Move up |
| ↓ / j | Move down |
| Enter | Open thread |
| q / Esc | Quit or go back |
| s | Sync all providers |
| / | Inline search |
| a | Archive or unarchive selected thread |
| A | Show or hide archived threads |
| n | Rename selected thread |
| d | Delete selected thread |
| H | Set up ingestion hooks |
| PgUp / PgDn | Scroll thread |
| Space | Toggle selection (first-run provider picker, delete picker) |
| h | Handoff current thread to another provider |
| r | Resume current thread in its native provider CLI |

## Installation

```bash
go install github.com/zuhailkhan/threadman/cmd/threadman@latest
```

This installs `threadman` to your Go bin directory (usually `~/go/bin`), which needs to be on your `PATH`. Run the same command to update.

To build from source instead:

```bash
git clone https://github.com/zuhailkhan/threadman
cd threadman
go build -o threadman ./cmd/threadman
```

> Requires Go 1.26.2 or newer and a C compiler (`gcc`) — `go-sqlite3` uses CGO.

## Project Structure

```
threadman/
├── cmd/threadman/        # Entry point
├── internal/
│   ├── domain/           # Core models: Thread, Message, Role
│   ├── ports/            # Interfaces: ThreadProvider, ThreadRepository
│   ├── providers/        # Claude, OpenCode integrations
│   ├── storage/sqlite/   # SQLite persistence (ent ORM)
│   ├── sync/             # Sync service: discover → hydrate → upsert
│   ├── hooks/            # Provider hook and plugin setup
│   ├── tui/              # Bubble Tea interface
│   └── cli/              # Cobra commands + TUI launcher
└── go.mod
```

## Tech Stack

- **TUI:** charmbracelet/bubbletea + lipgloss + bubbles
- **CLI:** spf13/cobra
- **Storage:** SQLite via ent ORM

## License

[MIT](LICENSE)
