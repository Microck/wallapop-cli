package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ServerName is the key the entry is written under in every harness config.
const ServerName = "wallapop"

// Harnesses are the agent harnesses `wallapop mcp install` knows, in the order
// they are listed in help.
var Harnesses = []string{"claude-code", "codex", "cursor"}

// Change is what one install did, printed so the person can see the file and
// the decision rather than trusting a silent write.
type Change struct {
	Harness string   `json:"harness"`
	Path    string   `json:"path"`
	Action  string   `json:"action"` // added, updated or unchanged
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

const (
	actionAdded     = "added"
	actionUpdated   = "updated"
	actionUnchanged = "unchanged"
)

// Install writes the stdio server entry into the harness's config, leaving
// every other entry (and, in TOML, every comment) alone. Running it twice
// reports "unchanged" the second time.
func Install(harness, command string, args []string) (Change, error) {
	path, err := configPath(harness)
	if err != nil {
		return Change{}, err
	}
	ch := Change{Harness: harness, Path: path, Command: command, Args: args}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ch, err
	}
	if harness == "codex" {
		ch.Action, err = installTOML(path, command, args)
	} else {
		ch.Action, err = installJSON(path, command, args)
	}
	return ch, err
}

// configPath locates the harness config. Each harness's own home variable wins,
// so an install lands where that harness actually reads from.
func configPath(harness string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch harness {
	case "claude-code":
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return filepath.Join(dir, ".claude.json"), nil
		}
		return filepath.Join(home, ".claude.json"), nil
	case "codex":
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "config.toml"), nil
		}
		return filepath.Join(home, ".codex", "config.toml"), nil
	case "cursor":
		return filepath.Join(home, ".cursor", "mcp.json"), nil
	}
	return "", fmt.Errorf("unknown harness %q. Choose one of %s", harness, strings.Join(Harnesses, ", "))
}

// installJSON edits a harness config that is one JSON document. The file is
// decoded into a generic map and re-encoded, so keys this CLI knows nothing
// about survive; JSON carries no comments to lose.
func installJSON(path, command string, args []string) (string, error) {
	doc := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return "", fmt.Errorf("%s is not valid json: %w", path, err)
		}
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	// Only command and args are this CLI's to write: an entry may also carry
	// env, timeouts or harness-specific keys the person put there, and an
	// install must not throw them away.
	entry, _ := servers[ServerName].(map[string]any)
	action := actionAdded
	if entry != nil {
		if entry["command"] == command && reflect.DeepEqual(entry["args"], toAny(args)) {
			return actionUnchanged, nil
		}
		action = actionUpdated
	} else {
		entry = map[string]any{}
	}
	entry["command"] = command
	entry["args"] = toAny(args)
	servers[ServerName] = entry
	doc["mcpServers"] = servers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	// 0600: a harness config holds the account's other server credentials.
	return action, writeFile(path, append(out, '\n'), 0o600)
}

// tomlTable matches the table header this CLI owns, bare or quoted;
// tomlNextTable finds where that table ends, at the next header.
var (
	tomlTable     = regexp.MustCompile(`(?m)^\[mcp_servers\.(?:` + ServerName + `|"` + ServerName + `")\]\s*$`)
	tomlNextTable = regexp.MustCompile(`(?m)^\[`)
)

// installTOML edits codex's config.toml as text. A generic parse-and-remarshal
// would drop the user's comments and reorder their file, so only the lines of
// this one table are touched.
func installTOML(path, command string, args []string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	body := string(raw)
	entryExists := false
	if strings.TrimSpace(body) != "" {
		var doc map[string]any
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return "", fmt.Errorf("%s is not valid toml: %w", path, err)
		}
		if servers, ok := doc["mcp_servers"].(map[string]any); ok {
			if entry, ok := servers[ServerName].(map[string]any); ok {
				entryExists = true
				if entry["command"] == command && reflect.DeepEqual(entry["args"], toAny(args)) {
					return actionUnchanged, nil
				}
			}
		}
	}

	block := tomlBlock(command, args)
	loc := tomlTable.FindStringIndex(body)
	// TOML spells one table many ways (["mcp_servers"."wallapop"], an inline
	// table, quoted keys). The parse sees them all, this rewrite only sees the
	// plain header; appending a second table on top of one of the others would
	// leave the file invalid, so say so instead.
	if loc == nil && entryExists {
		return "", fmt.Errorf("%s already has an mcp_servers.%s entry in a spelling this command will not rewrite. Edit it by hand: command = %s, args = [%s]",
			path, ServerName, strconv.Quote(command), strings.Join(quoteAll(args), ", "))
	}
	if loc == nil {
		joined := strings.TrimRight(body, "\n")
		if joined != "" {
			joined += "\n\n"
		}
		return actionAdded, writeFile(path, []byte(joined+block), 0o600)
	}
	// Replace from the table header to the next top-level header, so the
	// entry's own keys go and nothing after them does.
	tail := body[loc[1]:]
	end := len(body)
	if next := tomlNextTable.FindStringIndex(tail); next != nil {
		end = loc[1] + next[0]
	}
	return actionUpdated, writeFile(path, []byte(body[:loc[0]]+block+body[end:]), 0o600)
}

func tomlBlock(command string, args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = strconv.Quote(a)
	}
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n", ServerName, strconv.Quote(command), strings.Join(quoted, ", "))
}

// writeFile replaces the file atomically, keeping the mode it already had.
// A config kept by a dotfile manager is usually a symlink, so the target is
// rewritten rather than replaced by a regular file.
func writeFile(path string, content []byte, mode os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ToolNames lists the tools this server exposes, for help text and docs.
func ToolNames() []string {
	names := make([]string, 0, len(listTools()))
	for _, t := range listTools() {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}
