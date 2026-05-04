# threadman

`threadman` is a simple CLI-based tool designed to gather threads from all known chatbots and CLI tools into one centralized location.

## Features (Planned)
- Gather threads from various AI chatbots.
- Centralized view of conversations.
- CLI-based management.

## Project Structure
`threadman` follows Clean Architecture principles for scalability and maintainability.

```text
threadman/
├── cmd/
│   └── threadman/
│       └── main.go           # The entry point.
├── internal/
│   ├── domain/               # Core business models.
│   ├── ports/                # Interface definitions.
│   ├── providers/            # Chatbot integrations.
│   ├── storage/              # SQLite storage implementation.
│   └── cli/                  # CLI presentation layer. 
├── go.mod
└── README.md
```

## Tech Stack
- **CLI:** spf13/cobra
- **Storage:** SQLite

## Getting Started
Project is currently in its initial phase.
