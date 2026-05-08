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
	case "gemini":
		return SetupGemini()
	case "opencode":
		return SetupOpencode()
	default:
		return fmt.Errorf("unknown provider: %s", provider)
	}
}

func SetupAll() []error {
	var errs []error
	for _, fn := range []func() error{SetupClaude, SetupGemini, SetupOpencode} {
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

func SetupGemini() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return mergeCommandHook(
		filepath.Join(home, ".gemini", "settings.json"),
		"AfterAgent",
		"threadman ingest --provider gemini",
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

	var hooksMap map[string][]hookMatcher
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &hooksMap); err != nil {
			return fmt.Errorf("parse hooks in %s: %w", settingsPath, err)
		}
	}
	if hooksMap == nil {
		hooksMap = map[string][]hookMatcher{}
	}

	for _, m := range hooksMap[eventName] {
		for _, h := range m.Hooks {
			if h.Command == command {
				return nil
			}
		}
	}

	hooksMap[eventName] = append(hooksMap[eventName], hookMatcher{
		Matcher: "",
		Hooks:   []hookEntry{{Type: "command", Command: command}},
	})

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

const opencodePluginSrc = `import type { Plugin } from "@opencode-ai/plugin"
import { execFileSync } from "child_process"

export const ThreadmanPlugin: Plugin = async () => {
  return {
    event: async ({ event }: { event: { type: string; properties: { sessionID: string } } }) => {
      if (event.type !== "session.idle") return
      try {
        execFileSync("threadman", ["ingest", "--provider", "opencode"], {
          input: JSON.stringify({ session_id: event.properties.sessionID }),
          stdio: ["pipe", "ignore", "inherit"],
        })
      } catch (_) {}
    },
  }
}
`
