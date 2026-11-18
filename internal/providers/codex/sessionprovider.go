package codex

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// defaultModelProvider is the provider Codex uses when nothing else is set.
const defaultModelProvider = "openai"

// sessionModelProvider is the model provider to record in a session threadman
// writes. Codex resumes a session only with the provider it was recorded with,
// and copies that provider into its own index the first time it sees the
// session, so it has to be right when the file is written: a session with no
// provider can't be resumed at all: Codex reports that the chat uses an
// unavailable model provider with an empty name.
//
// It is the provider Codex itself would start with: the one set in config.toml
// (directly, or in the active profile), else the provider of the newest
// session the user ran in Codex, else Codex's default.
func sessionModelProvider(home string) string {
	if p := configModelProvider(filepath.Join(home, "config.toml")); p != "" {
		return p
	}
	if p := newestSessionProvider(filepath.Join(home, "sessions")); p != "" {
		return p
	}
	return defaultModelProvider
}

// configModelProvider reads model_provider from Codex's config.toml: the
// top-level key, or the one in [profiles.<name>] when a top-level profile is
// set. It reads only these simple string keys rather than parsing TOML.
func configModelProvider(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var provider, profile string
	profileProviders := map[string]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := stringSetting(line)
		if !ok {
			continue
		}
		switch {
		case section == "" && key == "model_provider":
			provider = value
		case section == "" && key == "profile":
			profile = value
		case strings.HasPrefix(section, "profiles.") && key == "model_provider":
			name := strings.Trim(strings.TrimPrefix(section, "profiles."), `"'`)
			profileProviders[name] = value
		}
	}
	if p := profileProviders[profile]; profile != "" && p != "" {
		return p
	}
	return provider
}

// stringSetting parses a `key = "value"` line, with a basic or literal string.
func stringSetting(line string) (key, value string, ok bool) {
	k, v, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(k)
	v = strings.TrimSpace(v)
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if len(v) < 2 || (v[0] != '"' && v[0] != '\'') || v[len(v)-1] != v[0] {
		return "", "", false
	}
	return key, v[1 : len(v)-1], true
}

// newestSessionProvider returns the model_provider recorded by the newest
// session Codex itself wrote. Sessions threadman wrote are skipped.
func newestSessionProvider(root string) string {
	type rollout struct{ path, stamp string }
	var rollouts []rollout
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if name, ok := parseRolloutName(d.Name()); ok {
			rollouts = append(rollouts, rollout{path, name.stamp})
		}
		return nil
	})
	sort.Slice(rollouts, func(i, j int) bool { return rollouts[i].stamp > rollouts[j].stamp })

	for i, r := range rollouts {
		if i == 50 {
			break
		}
		if meta, ok := readSessionMeta(r.path); ok && meta.Originator != "threadman" && meta.ModelProvider != "" {
			return meta.ModelProvider
		}
	}
	return ""
}

// readSessionMeta reads a rollout's first line, its session_meta.
func readSessionMeta(path string) (sessionMeta, bool) {
	r, done, err := openRollout(path)
	if err != nil {
		return sessionMeta{}, false
	}
	defer done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	if !sc.Scan() {
		return sessionMeta{}, false
	}
	var line rolloutLine
	var meta sessionMeta
	if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "session_meta" || json.Unmarshal(line.Payload, &meta) != nil {
		return sessionMeta{}, false
	}
	return meta, true
}
