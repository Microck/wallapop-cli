package mcp

import (
	"bytes"
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
	// The home directory is only resolved where it is actually needed: a
	// container or service account with CODEX_HOME set but no HOME still has
	// everything this install requires.
	inHome := func(parts ...string) (string, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(append([]string{home}, parts...)...), nil
	}
	switch harness {
	case "claude-code":
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return filepath.Join(dir, ".claude.json"), nil
		}
		return inHome(".claude.json")
	case "codex":
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "config.toml"), nil
		}
		return inHome(".codex", "config.toml")
	case "cursor":
		return inHome(".cursor", "mcp.json")
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
		// UseNumber: a float64 round-trip would round any integer setting
		// beyond 2^53 and quietly change a value this install promised to keep.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return "", fmt.Errorf("%s is not valid json: %w", path, err)
		}
		// A document of literal `null` decodes to a nil map.
		if doc == nil {
			doc = map[string]any{}
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
// tomlNextTable finds where that table ends, at the next header. TOML allows
// whitespace before a header, so both accept it.
var (
	tomlTable     = regexp.MustCompile(`(?m)^[ \t]*\[mcp_servers\.(?:` + ServerName + `|"` + ServerName + `")\][ \t]*$`)
	tomlNextTable = regexp.MustCompile(`(?m)^[ \t]*\[`)
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
	// Rewrite from the table header to the next top-level header. Only the
	// command and args lines are this CLI's: codex keeps its own per-server
	// settings (startup_timeout_sec, enabled, tool filters) in the same table,
	// and they stay, comments included.
	end := tableEnd(body, loc[0], loc[1])
	kept := strings.TrimLeft(stripOwnedKeys(body[loc[1]:end]), "\n")
	return actionUpdated, writeFile(path, []byte(body[:loc[0]]+block+kept+body[end:]), 0o600)
}

// tableEnd returns the offset where the table starting at start (whose header
// ends at afterHeader) ends. A line opening with `[` is only a header if what
// comes before it is a complete table, so the parser decides: a `[` inside a
// multi-line string is left where it is.
func tableEnd(body string, start, afterHeader int) int {
	for _, m := range tomlNextTable.FindAllStringIndex(body[afterHeader:], -1) {
		end := afterHeader + m[0]
		var parsed map[string]any
		if toml.Unmarshal([]byte(body[start:end]), &parsed) == nil {
			return end
		}
	}
	return len(body)
}

// ownedKey matches the two assignments this CLI writes, in each of the
// spellings TOML allows for a bare key.
var ownedKey = regexp.MustCompile(`^\s*(?:command|args|"command"|"args"|'command'|'args')\s*=`)

// stripOwnedKeys drops the command and args assignments from one table's body,
// a multi-line array value included, and returns what is left untouched.
func stripOwnedKeys(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		// A line only starts an assignment when everything above it is a
		// complete parse. Inside a multi-line string, a line reading
		// `command = ...` is somebody's text, not a key.
		if !ownedKey.MatchString(lines[i]) || !parsesTOML(strings.Join(lines[:i], "\n")) {
			kept = append(kept, lines[i])
			continue
		}
		i = assignmentEnd(lines, i)
	}
	return strings.Join(kept, "\n")
}

func parsesTOML(s string) bool {
	var parsed map[string]any
	return toml.Unmarshal([]byte(s), &parsed) == nil
}

// assignmentEnd returns the index of the last line of the assignment starting
// at start. A value may run over several lines, and a bracket inside a string
// or a comment means nothing, so the TOML parser decides where it ends rather
// than a character count: the first prefix that parses is the whole value.
func assignmentEnd(lines []string, start int) int {
	for end := start; end < len(lines); end++ {
		if parsesTOML(strings.Join(lines[start:end+1], "\n")) {
			return end
		}
	}
	return start
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
	} else if target, ok := followDanglingLink(path); ok {
		// A link whose target does not exist yet still says where the file
		// belongs; replacing the link with a regular file would break the
		// dotfile manager that made it.
		path = target
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
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

// followDanglingLink walks a chain of symlinks to the name at its end, which
// EvalSymlinks refuses to report once that name does not exist. The hop limit
// is the usual guard against a loop.
func followDanglingLink(path string) (string, bool) {
	found := false
	for hop := 0; hop < 32; hop++ {
		next, err := os.Readlink(path)
		if err != nil {
			return path, found
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(path), next)
		}
		path, found = next, true
	}
	return path, found
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
