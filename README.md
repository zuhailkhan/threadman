# threadman

`threadman` is a terminal interface for managing AI conversation threads across Claude and OpenCode — browse, search, hand off between providers, and sync across devices.

## Features

- **Interactive TUI** — full-screen thread browser with colors, navigation, and inline search
- **Multi-provider sync** — reads local session files from Claude Code and OpenCode
- **Thread reader** — scrollable message view with You/Assistant labels
- **Handoff** — transfer any conversation to a different provider's native session format and open it immediately
- **Thread grouping** — threads handed off across providers are grouped into a single row with provider icon clusters `[C O]`
- **CLI subcommands** — `sync`, `list`, `search` for scripting


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

# Sync all providers to local database
threadman sync

# List threads
threadman list

# Search threads
threadman search <query>

# Print a thread by its number in `threadman list`
threadman show <number>

# Install per-response ingestion hooks
threadman setup-hooks
```

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
| h | Handoff current thread to another provider |
| r | Resume current thread in its native provider CLI |

## Installation

```bash
git clone https://github.com/zuhailkhan/threadman
cd threadman
go build -o threadman ./cmd/threadman
```

> Requires a C compiler (`gcc`) — `go-sqlite3` uses CGO.

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
