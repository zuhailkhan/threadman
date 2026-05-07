# threadman

`threadman` aggregates AI conversation threads from Claude, Gemini, and OpenCode into a single interactive terminal UI.

## Features

- **Interactive TUI** — full-screen thread browser with colors, navigation, and inline search
- **Multi-provider sync** — reads local session files from Claude Code, Gemini CLI, and OpenCode
- **Thread reader** — scrollable message view with You/Assistant labels
- **CLI subcommands** — `sync`, `list`, `search` for scripting


<img width="1920" height="1045" alt="image" src="https://github.com/user-attachments/assets/94d4fbc4-be9c-40f6-bb5a-56d487347d17" />


## Supported Providers

| Provider | Source |
|----------|--------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| Gemini CLI | `~/.gemini/tmp/*/conversation_history.json` |
| OpenCode | `~/.local/share/opencode/*/conversation.json` |

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
| PgUp / PgDn | Scroll thread |

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
│   ├── providers/        # Claude, Gemini, OpenCode integrations
│   ├── storage/sqlite/   # SQLite persistence (ent ORM)
│   ├── sync/             # Sync service: discover → hydrate → upsert
│   └── cli/              # Cobra commands + TUI launcher
└── go.mod
```

## Tech Stack

- **TUI:** charmbracelet/bubbletea + lipgloss + bubbles
- **CLI:** spf13/cobra
- **Storage:** SQLite via ent ORM
