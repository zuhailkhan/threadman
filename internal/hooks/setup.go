package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func Setup(provider string) error {
	switch provider {
	case "claude":
		return SetupClaude()
	case "opencode":
		return SetupOpencode()
	case "pi":
		return SetupPi()
	default:
		return fmt.Errorf("unknown provider: %s", provider)
	}
}

func SetupAll() []error {
	var errs []error
	for _, fn := range []func() error{SetupClaude, SetupOpencode, SetupPi} {
		if err := fn(); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func SetupClaude() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return mergeCommandHook(
		filepath.Join(home, ".claude", "settings.json"),
		"Stop",
		"threadman ingest --provider claude",
	)
}

func SetupOpencode() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	pluginDir := filepath.Join(home, ".config", "opencode", "plugins")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		return fmt.Errorf("create opencode plugins dir: %w", err)
	}

	pluginPath := filepath.Join(pluginDir, "threadman.ts")
	if err := os.WriteFile(pluginPath, []byte(opencodePluginSrc), 0600); err != nil {
		return fmt.Errorf("write opencode plugin: %w", err)
	}

	return mergeOpencodePlugin(
		filepath.Join(home, ".config", "opencode", "opencode.json"),
		pluginPath,
	)
}

// SetupPi installs a global pi extension. pi auto-discovers extensions in
// <agent dir>/extensions, so there is no config file to merge into.
func SetupPi() error {
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		agentDir = filepath.Join(home, ".pi", "agent")
	}

	extDir := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(extDir, 0700); err != nil {
		return fmt.Errorf("create pi extensions dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "threadman.ts"), []byte(piExtensionSrc), 0600); err != nil {
		return fmt.Errorf("write pi extension: %w", err)
	}
	return nil
}

// --- shared JSON hook types ---

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// mergeCommandHook adds a command hook entry under eventName in a settings.json,
// preserving all other top-level keys. No-ops if the command is already present.
func mergeCommandHook(settingsPath, eventName, command string) error {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
	}

	// Existing hooks stay as raw JSON so fields threadman doesn't model, such as
	// timeout, survive the rewrite.
	hooksMap := map[string]json.RawMessage{}
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &hooksMap); err != nil {
			return fmt.Errorf("parse hooks in %s: %w", settingsPath, err)
		}
	}

	var matchers []json.RawMessage
	if eventRaw, ok := hooksMap[eventName]; ok {
		if err := json.Unmarshal(eventRaw, &matchers); err != nil {
			return fmt.Errorf("parse %s hooks in %s: %w", eventName, settingsPath, err)
		}
	}

	for _, rawMatcher := range matchers {
		var m hookMatcher
		if err := json.Unmarshal(rawMatcher, &m); err != nil {
			return fmt.Errorf("parse %s hooks in %s: %w", eventName, settingsPath, err)
		}
		for _, h := range m.Hooks {
			if h.Command == command {
				return nil
			}
		}
	}

	added, err := json.Marshal(hookMatcher{
		Matcher: "",
		Hooks:   []hookEntry{{Type: "command", Command: command}},
	})
	if err != nil {
		return err
	}
	eventJSON, err := json.Marshal(append(matchers, added))
	if err != nil {
		return err
	}
	hooksMap[eventName] = eventJSON

	hooksJSON, err := json.Marshal(hooksMap)
	if err != nil {
		return err
	}
	raw["hooks"] = hooksJSON

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0600)
}

func mergeOpencodePlugin(configPath, pluginPath string) error {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read opencode config: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse opencode config: %w", err)
		}
	}

	var plugins []string
	if pluginsRaw, ok := raw["plugin"]; ok {
		if err := json.Unmarshal(pluginsRaw, &plugins); err != nil {
			return fmt.Errorf("parse plugin list: %w", err)
		}
	}
	for _, p := range plugins {
		if p == pluginPath {
			return nil
		}
	}
	plugins = append(plugins, pluginPath)

	pluginsJSON, err := json.Marshal(plugins)
	if err != nil {
		return err
	}
	raw["plugin"] = pluginsJSON

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(configPath, out, 0600)
}

const opencodePluginSrc = `import { execFileSync } from "child_process"

function ingest(sessionID: string) {
  try {
    execFileSync("threadman", ["ingest", "--provider", "opencode"], {
      input: JSON.stringify({ session_id: sessionID }),
      stdio: ["pipe", "ignore", "inherit"],
      windowsHide: true,
    })
  } catch (_) {}
}

// One module that loads on both plugin generations, so it needs no
// @opencode-ai/plugin dependency whatever opencode version is installed:
//   v1 (<2.0)  calls server() and delivers events through the event hook
//   v2 (>=2.0) calls setup() and exposes ctx.event.subscribe()
export default {
  id: "threadman",
  server: async () => ({
    event: async ({ event }: any) => {
      if (event?.type === "session.idle") ingest(event.properties.sessionID)
    },
  }),
  async setup(ctx: any) {
    const controller = new AbortController()
    void (async () => {
      try {
        for await (const event of ctx.event.subscribe({ types: ["session.idle"], signal: controller.signal })) {
          const id = event?.data?.sessionID ?? event?.properties?.sessionID
          if (id) ingest(id)
        }
      } catch (_) {}
    })()
    return () => controller.abort()
  },
}
`

// agent_settled is pi's final "this run is over" event, so the thread is
// ingested once per response rather than once per retry or tool turn.
const piExtensionSrc = `import { spawn } from "node:child_process"

export default function (pi: any) {
  pi.on("agent_settled", async (_event: unknown, ctx: any) => {
    const file = ctx.sessionManager.getSessionFile()
    if (!file) return
    try {
      const child = spawn("threadman", ["ingest", "--provider", "pi"], {
        stdio: ["pipe", "ignore", "ignore"],
        windowsHide: true,
      })
      child.on("error", () => {})
      child.stdin.on("error", () => {})
      child.stdin.end(JSON.stringify({
        session_id: ctx.sessionManager.getSessionId(),
        transcript_path: file,
        cwd: ctx.cwd,
      }))
    } catch (_) {}
  })
}
`
