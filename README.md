# threadman

`threadman` is a terminal interface for managing AI conversation threads across Claude Code, OpenCode, pi, Antigravity and Codex — browse, search, and hand off a conversation from one provider to another.

threadman reads the session files your CLIs already write and indexes them into a SQLite database at `~/.local/share/threadman/threads.db`. The only network calls it makes are to the summarization LLM you configure for summary handoffs; with a local model, nothing leaves your machine.

That database is a durable copy. When a tool deletes a session, for example when Claude Code clears out old ones, threadman keeps the conversation: it stays readable and searchable, marked as deleted in that tool, and can be handed off to continue it in a new session.

## Features

- **Interactive TUI** — thread list grouped by date with a preview pane, tool tabs, and search across titles and messages; adapts to light and dark terminals
- **Multi-provider import** — reads local session files from Claude Code, OpenCode, pi, Antigravity and Codex; first run lets you pick which providers to import
- **Live updates** — optional per-response hooks ingest new messages as you chat, and the TUI refreshes automatically
- **Thread reader** — conversation view with an outline of your prompts to jump between, timestamps, code blocks and tool calls
- **Handoff** — transfer any conversation to a different provider's native session format and open it immediately, as the full transcript or as an LLM-written summary
- **Merge** — bring several separate threads together into one new session, each briefed with its tool, project and dates
- **Thread grouping** — threads handed off across providers are grouped into a single row showing each tool's icon; a tool that has deleted its session shows dimmed
- **Durable history** — conversations survive their tool deleting the session
- **CLI subcommands** — `sync`, `list`, `search`, `show` for scripting


<img width="1177" height="785" alt="image" src="https://github.com/user-attachments/assets/12e5b02a-e2af-4103-9d2e-1a67faba489e" />

## Supported Providers

| Provider | Source |
|----------|--------|
| Claude Code | `~/.claude/projects/**/*.jsonl` |
| OpenCode (1.x and 2.x) | `~/.local/share/opencode/opencode.db` |
| [pi](https://www.npmjs.com/package/@earendil-works/pi-coding-agent) | `~/.pi/agent/sessions/**/*.jsonl` |
| Antigravity CLI (read and resume only) | `~/.gemini/antigravity-cli/conversations/*.db` |
| Codex | `~/.codex/sessions/**/rollout-*.jsonl` (and `.jsonl.zst`) |

Handoff works in both directions with pi: a thread handed to pi is written as a native pi session and opened with `pi --session <id>`. For branched pi sessions, only the active branch is shown. `PI_CODING_AGENT_DIR` and `PI_CODING_AGENT_SESSION_DIR` are respected.

Handoff works in both directions with Codex: a thread handed to Codex is written as a native session and opened with `codex resume <id>`. `CODEX_HOME` is respected. Sessions Codex compressed to `.jsonl.zst` are read as they are; sub-agent sessions and archived sessions are not imported. There is no per-response hook for Codex yet, so press `s` to pick up new messages.

Antigravity conversations can be browsed, searched, resumed (`agy --conversation <id>`) and handed off to another provider, but not handed off *to* or deleted: Antigravity stores them in an undocumented binary format that threadman only reads. There is no per-response hook for it yet, so press `s` to pick up new messages.

> **Gemini CLI support has been removed.** Google replaced Gemini CLI with Antigravity CLI, so threadman no longer syncs from or hands off to it; Antigravity CLI conversations are imported instead. Gemini threads already in your local database stay listed and readable.

## Usage

```bash
# Launch interactive TUI
threadman

# Sync all providers to local database (--provider claude|opencode for one)
threadman sync

# List threads, most recently active first (--provider to filter)
threadman list

# Search threads
threadman search <query>

# Print a thread by its number in `threadman list`, or by its thread ID
# (use the same --provider as the list you took the number from)
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
| Full transcript | Every message |

The original thread is never changed. Whatever the mode, the new session holds a single message from threadman: a handoff block that says where the conversation came from, how to read the original (`threadman show <thread-id>`), and then the brief and messages, each labelled with who wrote it. The other model's replies are not written as the new model's own turns, so it reads them as an account from another tool rather than as things it said.

In the summary modes the newest messages are kept word for word after the brief, since those carry the task in progress. The kept messages always start on one of yours, so a few more than 6 or 2 may be kept. A thread with nothing older than that is handed over whole.

### Tool history

threadman keeps each turn's tool calls (the tool, its input such as the command run, its output up to 2,000 characters, and whether it failed) from Claude Code, OpenCode and pi. The thread reader shows one dim line per call, and `threadman show` prints them in full.

Handoff blocks carry them too, under the turn that made them:

```
[Assistant · OpenCode · 2026-11-16 10:02]
Let me check.

→ shell (failed): go build ./...
    internal/sync/service.go:88:3: undefined: pruneStale
```

Each output is cut to 800 characters. If a block would still be larger than about 300 KB (around 75k tokens), outputs are cut to 200 characters and then left out, keeping every call, and the block says so. A conversation too long even then is better handed off as a summary: the mode dialog shows the full transcript's estimated size in tokens and recommends a summary when it is over that limit. The summarizer sees each call with a short output, so briefs can say what was actually run.

Every handoff is recorded in threadman's database with the exact text sent. When the new session is synced, threadman recognises its block: the thread reader shows it as a divider, search skips it, and it doesn't become the session's title.

### One conversation across tools

Sessions linked by handoffs form one conversation. Opening it shows a single timeline: every session's messages in time order, each tagged with its tool, and a divider wherever the conversation was handed off.

The handoff dialog lists every tool with what it would get:

| Status | What happens |
|--------|--------------|
| `new session` | The tool has no session in this conversation yet, so one is created holding the whole conversation |
| `N new messages` | The tool already has a session, so only the messages written elsewhere since its last handoff are appended to it, and you resume that same session |
| `up to date` | Nothing to send |

So a conversation that goes from Claude Code to OpenCode and back stays one session in each tool: Claude Code's own turns, tool calls included, are untouched, and OpenCode's turns arrive after them as a handoff block. If you kept working in Claude Code after handing off, it still gets everything OpenCode did meanwhile.

OpenCode 2.x has no command for adding to a session, so threadman exports the session, deletes it and imports it again under the same ID with the block added. The export is saved to `~/.local/share/threadman/backups/opencode/` first, and if the import fails or loses messages, the original is imported back. A session with subagent sessions is refused, since deleting it would delete those too.

Close a session in its tool before appending to it: a running session doesn't see the new messages and keeps writing from where it was.

The two summary modes need an LLM. Without one, or when it cannot be reached, only the full transcript is offered.

Press `L` in the thread list or in the mode dialog to set the LLM up in the TUI: choose a provider with ←/→, fill in the model, and optionally a base URL and API key, then press Enter to save and check the connection. The same settings are available from the command line:

```bash
threadman llm set --provider <name> [--model <model>] [--base-url <url>] [--api-key <key>] [--context <tokens>]
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

threadman checks the LLM when it starts and shows the result in the status bar, and checks again each time the mode dialog opens.

A conversation longer than the model's context window is summarized in parts: threadman reads it in consecutive pieces that fit, has the model take notes on each, then writes the brief from the notes. It learns the window from LM Studio and Ollama (the length the model is loaded with) and assumes 200k tokens for Claude, 128k for OpenAI and 64k for DeepSeek. For any other server, or to override, set it with `threadman llm set ... --context <tokens>`; `threadman llm test` shows the window in use. If a request is still rejected as too long, threadman takes the limit from the error and tries again in smaller parts. A failed handoff or merge shows its full error in a dialog.

### Merging threads

Work on one thing often ends up spread over separate sessions: one started fresh when the context filled up, another tried in a different tool. To continue from all of them, select their rows in the thread list with Space and press `m`, then choose the tool for the new session.

The new session holds one handoff block with a section per thread, oldest first. Each section gives the thread's tool, project, dates and message count, the command to read it in full, and a detailed brief of it (goal, current state, decisions in force, rejected approaches, open questions, next steps). Every thread gets its own brief, so a short one isn't drowned out by a long one. Only the newest thread's last 2 messages are kept word for word, since that is where the work stopped. The block says the threads ran separately and that where they disagree, the later one is more likely to be current.

A merge always writes a new session, which runs in the newest thread's workspace. It needs an LLM, with one call per thread, or more for a thread summarized in parts. With a local model the threads are summarized one at a time, since requests sent to it together can share one context. Up to 8 threads can be merged at once. A row that is already a conversation across tools counts as one thread. The threads merged are left unchanged and stay in the list; the new session is a conversation of its own, titled after the newest thread with "(merged from N)".

### TUI Keybindings

Press `?` in the thread list or the reader to see every key. The footer shows the keys that work on the current screen.

| Key | Action |
|-----|--------|
| ↑ / k, ↓ / j | Move, or scroll the reader |
| g / G | Go to the top or bottom |
| Enter | Open thread |
| q / Esc | Quit or go back; Esc in the thread list clears the selection, then the search |
| / | Search titles and message text |
| Tab / Shift+Tab | Show one tool's threads, cycling through the tools |
| s | Sync all providers |
| a | Archive or unarchive selected thread |
| A | Show or hide archived threads |
| n | Rename selected thread |
| d | Delete selected thread |
| H | Set up ingestion hooks |
| Space | Toggle selection (thread list, first-run picker, delete picker) |
| m | Merge the selected threads into a new session |
| [ / ] | Jump to the previous or next of your prompts in the reader |
| PgUp / PgDn | Scroll the reader a page |
| h | Handoff current thread to another provider |
| L | Set up the LLM used for handoff summaries |
| r | Resume current thread in its native provider CLI |
| ? | Show every key |

Each tool has an icon: ✻ Claude Code, ▣ OpenCode, π pi, ▲ Antigravity, ◎ Codex and ✦ Gemini. They are plain Unicode and work in any terminal. With a [Nerd Font](https://www.nerdfonts.com) 3.5 or newer, set `THREADMAN_ICONS=nerd` (or `NERD_FONT=1`) to use its glyphs instead, including the Claude logo. A terminal can't tell threadman which font it uses, so this is a setting rather than detected.

## Roadmap

Providers are added one at a time. Planned, in no fixed order:

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
- [ ] Codex: per-response hook

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

> Requires Go 1.26.2 or newer. No C compiler is needed: threadman uses a pure-Go SQLite (`modernc.org/sqlite`).

## Project Structure

```
threadman/
├── cmd/threadman/        # Entry point
├── internal/
│   ├── domain/           # Core models: Thread, Message, Role
│   ├── ports/            # Interfaces: ThreadProvider, ThreadRepository
│   ├── providers/        # Claude, OpenCode, pi, Antigravity, Codex integrations
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
