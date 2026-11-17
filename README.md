# threadman

`threadman` is a terminal interface for managing AI conversation threads across Claude Code, OpenCode, pi and Antigravity — browse, search, and hand off a conversation from one provider to another.

Everything runs locally: threadman reads the session files your CLIs already write, indexes them into a SQLite database at `~/.local/share/threadman/threads.db`, and makes no network calls.

## Features

- **Interactive TUI** — full-screen thread browser with colors, navigation, and inline search
- **Multi-provider import** — reads local session files from Claude Code, OpenCode, pi and Antigravity; first run lets you pick which providers to import
- **Live updates** — optional per-response hooks ingest new messages as you chat, and the TUI refreshes automatically
- **Thread reader** — scrollable message view with You/Assistant labels
- **Handoff** — transfer any conversation to a different provider's native session format and open it immediately, as the full transcript or as an LLM-written summary
- **Thread grouping** — threads handed off across providers are grouped into a single row with provider icon clusters `[C O]`
- **CLI subcommands** — `sync`, `list`, `search`, `show` for scripting


<img width="1920" height="1045" alt="image" src="https://github.com/user-attachments/assets/94d4fbc4-be9c-40f6-bb5a-56d487347d17" />


## Supported Providers

| Provider | Source |
|----------|--------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenCode (1.x and 2.x) | `~/.local/share/opencode/opencode.db` |
| [pi](https://www.npmjs.com/package/@earendil-works/pi-coding-agent) | `~/.pi/agent/sessions/**/*.jsonl` |
| Antigravity CLI (read and resume only) | `~/.gemini/antigravity-cli/conversations/*.db` |

Handoff works in both directions with pi: a thread handed to pi is written as a native pi session and opened with `pi --session <id>`. For branched pi sessions, only the active branch is shown. `PI_CODING_AGENT_DIR` and `PI_CODING_AGENT_SESSION_DIR` are respected.

Antigravity conversations can be browsed, searched, resumed (`agy --conversation <id>`) and handed off to another provider, but not handed off *to* or deleted: Antigravity stores them in an undocumented binary format that threadman only reads. There is no per-response hook for it yet, so press `s` to pick up new messages.

> **Gemini CLI support has been removed.** Google replaced Gemini CLI with Antigravity CLI, so threadman no longer syncs from or hands off to it; Antigravity CLI conversations are imported instead. Gemini threads already in your local database stay listed and readable.

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

# Print a thread by its number in `threadman list`, or by its thread ID
threadman show <number|thread-id>

# Choose the LLM used to summarize threads on handoff, then check it
threadman llm set --provider ollama --model llama3.2
threadman llm test

# Install per-response ingestion hooks for Claude Code, OpenCode and pi (--provider to configure just one)
threadman setup-hooks
```

`setup-hooks` adds a `Stop` hook to `~/.claude/settings.json`, an OpenCode plugin at `~/.config/opencode/plugins/threadman.ts`, and a pi extension at `~/.pi/agent/extensions/threadman.ts`. All three call `threadman ingest`, which reads a hook payload from stdin; you normally don't run it yourself.

### Handoff modes

Pressing `h` in a thread asks for the target provider and then for a mode:

| Mode | What the new session contains |
|------|-------------------------------|
| Detailed summary | A brief of the older messages (goal, current state, decisions in force and why, rejected approaches, open questions, next steps), then the last 6 messages word for word |
| Light summary | A brief of the older messages in up to 200 words, then the last 2 messages word for word |
| Full transcript | Every message, as before |

The original thread is never changed. A summary handoff writes a new session that opens with the brief, which tells the new agent how to read the original (`threadman show <thread-id>`), followed by the newest messages unchanged, since those carry the task in progress. The kept messages always start on one of yours, so a few more than 6 or 2 may be kept. A thread with nothing older than that is handed over whole. Summaries are written from message text only, because threadman does not store tool calls or command output.

The two summary modes need an LLM. Without one, or when it cannot be reached, only the full transcript is offered.

Press `L` in the thread list or in the mode dialog to set the LLM up in the TUI: choose a provider with ←/→, fill in the model, and optionally a base URL and API key, then press Enter to save and check the connection. The same settings are available from the command line:

```bash
threadman llm set --provider <name> [--model <model>] [--base-url <url>] [--api-key <key>]
threadman llm show    # current settings
threadman llm test    # check the server, credentials and model
threadman llm clear
```

| Provider | Default address | API key |
|----------|-----------------|---------|
| `claude` | Anthropic API | `ANTHROPIC_API_KEY`, or `--api-key` |
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY`, or `--api-key` |
| `deepseek` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY`, or `--api-key` |
| `ollama` | `http://localhost:11434/v1` | none |
| `lmstudio` | `http://localhost:1234/v1` | none |

`--model` is required except for `claude` (defaults to `claude-opus-5-5`) and `deepseek` (defaults to `deepseek-chat`). Any other server that speaks the OpenAI chat-completions API works with `--provider openai --base-url <url>`. The hosted APIs bill per token, separately from any chat subscription; Ollama and LM Studio run locally. Settings are stored in `~/.config/threadman/config.json`. An API key you enter is kept in the system keyring (Windows Credential Manager, macOS Keychain, or the Secret Service on Linux) and not in that file; only when no keyring is available is it written to the file instead. `threadman llm show` reports which one is in use.

threadman checks the LLM when it starts and shows the result in the status bar, and checks again each time the mode dialog opens. A conversation too long for the model fails with the provider's error rather than being cut short; hand off the full transcript instead.

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
| L | Set up the LLM used for handoff summaries |
| r | Resume current thread in its native provider CLI |

## Roadmap

Providers are added one at a time. Planned, in no fixed order:

- [ ] Codex
- [ ] GitHub Copilot CLI
- [ ] Cursor
- [ ] Amp
- [ ] Cline
- [ ] Roo Code
- [ ] Kilo Code
- [ ] Kiro
- [ ] Crush
- [ ] Factory Droid
- [ ] Kimi CLI
- [ ] Qwen Code
- [ ] Antigravity: handoff target and per-response hook

## Installation

### Download a binary

Prebuilt binaries for Linux (amd64, arm64), macOS (Apple Silicon, Intel) and Windows (amd64) are on the [releases page](https://github.com/zuhailkhan/threadman/releases). Download the archive for your platform, extract it, and put `threadman` somewhere on your `PATH`. On Windows you can also download the bare `.exe` instead of the zip. Check it with `threadman --version`. Each release has a `checksums.txt` with SHA-256 sums.

The binaries are not code-signed. On macOS, clear the quarantine flag after extracting with `xattr -d com.apple.quarantine threadman`. On Windows, SmartScreen may warn the first time you run it.

### With Go

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
│   ├── providers/        # Claude, OpenCode, pi, Antigravity integrations
│   ├── storage/sqlite/   # SQLite persistence (ent ORM)
│   ├── sync/             # Sync service: discover → hydrate → upsert
│   ├── hooks/            # Provider hook and plugin setup
│   ├── llm/              # LLM config and clients (Claude, OpenAI-compatible)
│   ├── handoff/          # Handoff modes and thread summarization
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
