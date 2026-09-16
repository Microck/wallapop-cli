package cli_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/Microck/wallapop-cli/internal/cli"
	"github.com/Microck/wallapop-cli/internal/fakewallapop"
)

// mcpSession drives a real `wallapop mcp` server over the stdio transport:
// JSON-RPC lines into its stdin, lines out of its stdout. Nothing reaches into
// the mcp package; the tests see exactly what a harness would.
type mcpSession struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Reader
	exit chan int
	id   int
	once sync.Once
}

func (h *harness) mcpServer(args ...string) *mcpSession {
	h.t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	exit := make(chan int, 1)
	go func() {
		code := cli.Execute("test", append([]string{"mcp"}, args...), inR, outW, io.Discard)
		outW.Close()
		exit <- code
	}()
	s := &mcpSession{t: h.t, in: inW, out: bufio.NewReader(outR), exit: exit}
	h.t.Cleanup(s.close)
	return s
}

// close ends the session the way a harness does: shut stdin, expect exit 0.
func (s *mcpSession) close() {
	s.once.Do(func() {
		s.in.Close()
		select {
		case code := <-s.exit:
			if code != 0 {
				s.t.Errorf("mcp server exited %d", code)
			}
		case <-time.After(10 * time.Second):
			s.t.Error("mcp server did not exit after stdin closed")
		}
	})
}

func (s *mcpSession) write(msg map[string]any) {
	s.t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.in.Write(append(raw, '\n')); err != nil {
		s.t.Fatal(err)
	}
}

// request sends one request and returns the response object.
func (s *mcpSession) request(method string, params any) map[string]any {
	s.t.Helper()
	s.id++
	msg := map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	s.write(msg)
	line, err := s.out.ReadBytes('\n')
	if err != nil {
		s.t.Fatalf("reading response to %s: %v", method, err)
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		s.t.Fatalf("response to %s is not json: %v\n%s", method, err, line)
	}
	if resp["jsonrpc"] != "2.0" {
		s.t.Fatalf("response to %s is not jsonrpc 2.0: %s", method, line)
	}
	if id, _ := resp["id"].(float64); int(id) != s.id {
		s.t.Fatalf("response id %v, want %d: %s", resp["id"], s.id, line)
	}
	return resp
}

func (s *mcpSession) result(method string, params any) map[string]any {
	s.t.Helper()
	resp := s.request(method, params)
	if e, ok := resp["error"]; ok {
		s.t.Fatalf("%s failed: %v", method, e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("%s returned no result object: %v", method, resp)
	}
	return res
}

// callTool returns the tool's text content and whether it reported an error.
func (s *mcpSession) callTool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	res := s.result("tools/call", map[string]any{"name": name, "arguments": args})
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		s.t.Fatalf("tool %s returned no content", name)
	}
	// A failed command answers with what it printed and then its envelope, so
	// the last block is the one that matters either way.
	block, _ := content[len(content)-1].(map[string]any)
	if block["type"] != "text" {
		s.t.Fatalf("tool %s returned a %v block", name, block["type"])
	}
	text, _ := block["text"].(string)
	isError, _ := res["isError"].(bool)
	return text, isError
}

// callToolError returns the JSON-RPC error message for a call the protocol
// itself refuses (unknown tool, malformed arguments).
func (s *mcpSession) callToolError(name string, args map[string]any) string {
	s.t.Helper()
	resp := s.request("tools/call", map[string]any{"name": name, "arguments": args})
	e, ok := resp["error"].(map[string]any)
	if !ok {
		s.t.Fatalf("tools/call %s should have failed: %v", name, resp)
	}
	msg, _ := e["message"].(string)
	return msg
}

func (s *mcpSession) toolNames() []string {
	s.t.Helper()
	res := s.result("tools/list", nil)
	raw, _ := res["tools"].([]any)
	names := make([]string, 0, len(raw))
	for _, t := range raw {
		tool, _ := t.(map[string]any)
		name, _ := tool["name"].(string)
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			s.t.Errorf("tool %s has no object input schema: %v", name, schema)
		}
		if desc, _ := tool["description"].(string); desc == "" {
			s.t.Errorf("tool %s has no description", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestMCPHandshakeListsOnlyTheToolsThatMirrorReadCommands(t *testing.T) {
	h := newHarness(t)
	s := h.mcpServer()

	init := s.result("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
	if init["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocol version %v", init["protocolVersion"])
	}
	info, _ := init["serverInfo"].(map[string]any)
	if info["name"] != "wallapop" || info["version"] != "test" {
		t.Errorf("serverInfo %v", info)
	}
	caps, _ := init["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("tools capability missing: %v", caps)
	}

	// A notification takes no reply, malformed or not, so the next response
	// must be the ping's.
	s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	s.write(map[string]any{"jsonrpc": "2.0"})
	s.result("ping", nil)

	got := s.toolNames()
	want := []string{"item_show", "search", "user_show", "watch_check"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestMCPWithholdsSellerActionsAndChatUntilReceiveIsVerified(t *testing.T) {
	h := newHarness(t)
	s := h.mcpServer()
	for _, name := range []string{"item_sold", "item_delete", "item_reserve", "item_create", "item_edit"} {
		if msg := s.callToolError(name, nil); !strings.Contains(msg, "not offered") {
			t.Errorf("%s: %q", name, msg)
		}
	}
	// Chat is written but gated on issue #3; the refusal says so rather than
	// pretending the tool was misspelled.
	for _, name := range []string{"chat_list", "chat_show", "chat_send", "chat_start"} {
		if msg := s.callToolError(name, nil); !strings.Contains(msg, "#3") {
			t.Errorf("%s: %q", name, msg)
		}
	}
	if msg := s.callToolError("nonsense", nil); !strings.Contains(msg, "unknown tool") {
		t.Errorf("unknown tool message: %q", msg)
	}
	if resp := s.request("wallapop/please", nil); resp["error"] == nil {
		t.Errorf("unknown method should be a jsonrpc error: %v", resp)
	}
}

func TestMCPToolOutputIsTheSameJSONTheCommandPrints(t *testing.T) {
	h := newHarness(t)
	h.login()
	for i := 0; i < 5; i++ {
		h.fake.AddItem(bike("hash"+padHash(i), float64(100+i)))
	}
	s := h.mcpServer()

	text, isErr := s.callTool("search", map[string]any{
		"keywords":  "bici roja",
		"max_price": 500,
		"condition": []any{"good"},
		"sort":      "newest",
		"limit":     3,
	})
	if isErr {
		t.Fatalf("search tool failed: %s", text)
	}
	cmd := h.must("", "search", "bici", "roja", "--max-price", "500", "--condition", "good", "--sort", "newest", "--limit", "3")
	if text != strings.TrimRight(cmd.stdout, "\n") {
		t.Fatalf("tool output differs from the command\ntool: %s\ncmd:  %s", text, cmd.stdout)
	}
	q := h.fake.RequestsTo("/api/v3/search")[0].Query
	if q.Get("max_sale_price") != "500" || q.Get("condition") != "good" || q.Get("keywords") != "bici roja" {
		t.Fatalf("tool arguments did not reach the query: %v", q)
	}

	// Keywords ride behind --, so one that happens to name a subcommand or
	// start with a dash is still a search term.
	text, isErr = s.callTool("search", map[string]any{"keywords": "filters"})
	if isErr {
		t.Fatalf("search for a subcommand name failed: %s", text)
	}
	var page struct {
		Items []struct{ Hash string } `json:"items"`
	}
	decode(t, text, &page)
	if len(page.Items) == 0 {
		t.Fatalf("search ran the filters subcommand instead: %s", text)
	}

	it := h.fake.AddItem(bike("hashitemshow", 250))
	text, isErr = s.callTool("item_show", map[string]any{"item": it.Hash})
	if isErr {
		t.Fatalf("item_show failed: %s", text)
	}
	if text != strings.TrimRight(h.must("", "item", "show", it.Hash).stdout, "\n") {
		t.Fatalf("item_show differs from the command:\n%s", text)
	}

	text, isErr = s.callTool("user_show", map[string]any{"user": fakewallapop.OtherHash})
	if isErr {
		t.Fatalf("user_show failed: %s", text)
	}
	if text != strings.TrimRight(h.must("", "user", "show", fakewallapop.OtherHash).stdout, "\n") {
		t.Fatalf("user_show differs from the command:\n%s", text)
	}
}

func TestMCPFailedCommandComesBackAsTheErrorEnvelope(t *testing.T) {
	h := newHarness(t)
	h.login()
	s := h.mcpServer()
	text, isErr := s.callTool("item_show", map[string]any{"item": "hashmissing1"})
	if !isErr {
		t.Fatalf("missing item should be a tool error: %s", text)
	}
	var env struct {
		Code     int    `json:"code"`
		Category string `json:"category"`
		Message  string `json:"message"`
	}
	decode(t, text, &env)
	if env.Code != 4 || env.Category != "not_found" || env.Message == "" {
		t.Fatalf("envelope %+v", env)
	}
}

func TestMCPWatchCheckToolRunsTheProfilesWatches(t *testing.T) {
	h := newHarness(t)
	h.login()
	h.must("", "watch", "add", "search", "bici", "--name", "bici")
	h.fake.AddItem(bike("hashbrandnew", 80))

	s := h.mcpServer()
	text, isErr := s.callTool("watch_check", map[string]any{"all": true})
	if isErr {
		t.Fatalf("watch_check failed: %s", text)
	}
	var events []struct {
		Type  string
		Watch string
		Item  struct{ Hash string }
	}
	decode(t, text, &events)
	if len(events) != 1 || events[0].Type != "item.new" || events[0].Item.Hash != "hashbrandnew" {
		t.Fatalf("events %+v", events)
	}
	// The state is stored, so a second check through the server reports nothing.
	text, _ = s.callTool("watch_check", map[string]any{"names": []any{"bici"}})
	decode(t, text, &events)
	if len(events) != 0 {
		t.Fatalf("second check re-emitted: %+v", events)
	}
}

func TestMCPRejectsArgumentsTheSchemaDoesNotAllow(t *testing.T) {
	h := newHarness(t)
	h.login()
	s := h.mcpServer()
	if msg := s.callToolError("search", map[string]any{"colour": "red"}); !strings.Contains(msg, `unknown argument "colour"`) {
		t.Errorf("unknown argument: %q", msg)
	}
	if msg := s.callToolError("item_show", nil); !strings.Contains(msg, `"item" is required`) {
		t.Errorf("missing required: %q", msg)
	}
	if msg := s.callToolError("item_show", map[string]any{"item": 12}); !strings.Contains(msg, "must be a string") {
		t.Errorf("wrong type: %q", msg)
	}
	if msg := s.callToolError("search", map[string]any{"limit": 1.5}); !strings.Contains(msg, "whole number") {
		t.Errorf("fractional integer: %q", msg)
	}
}

func TestMCPServerActsAsTheProfileItWasStartedWith(t *testing.T) {
	h := newHarness(t)
	h.must("", "auth", "login", "--cookies", h.cookieFile(), "--profile", "work")
	h.fake.AddItem(bike("hashprofile1", 30))
	s := h.mcpServer("--profile", "work")
	// The location comes from the work profile; without it search exits 2.
	if text, isErr := s.callTool("search", map[string]any{"keywords": "bici"}); isErr {
		t.Fatalf("search as work profile failed: %s", text)
	}
}

// install

func TestMCPInstallIsIdempotentPerHarness(t *testing.T) {
	h := newHarness(t)
	claude := filepath.Join(h.home, ".claude.json")
	if err := os.WriteFile(claude, []byte(`{"numStartups":7,"mcpServers":{"other":{"command":"other-mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(h.home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	existingCodex := "# my codex config\nmodel = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"other-mcp\"\n"
	if err := os.WriteFile(codex, []byte(existingCodex), 0o600); err != nil {
		t.Fatal(err)
	}

	type change struct {
		Harness string
		Path    string
		Action  string
		Command string
		Args    []string
	}
	install := func(args ...string) change {
		var c change
		decode(t, h.must("", append([]string{"mcp", "install"}, args...)...).stdout, &c)
		return c
	}

	for _, tc := range []struct{ harness, path string }{
		{"claude-code", claude},
		{"codex", codex},
		{"cursor", filepath.Join(h.home, ".cursor", "mcp.json")},
	} {
		c := install(tc.harness)
		if c.Action != "added" || c.Path != tc.path {
			t.Fatalf("%s: %+v", tc.harness, c)
		}
		if c.Command == "" || strings.Join(c.Args, " ") != "mcp" {
			t.Fatalf("%s entry: %+v", tc.harness, c)
		}
		first, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if c := install(tc.harness); c.Action != "unchanged" {
			t.Fatalf("%s: second install said %q", tc.harness, c.Action)
		}
		again, _ := os.ReadFile(tc.path)
		if string(again) != string(first) {
			t.Fatalf("%s: second install rewrote the file\n%s\n%s", tc.harness, first, again)
		}
		if c := install(tc.harness, "--profile", "work"); c.Action != "updated" || strings.Join(c.Args, " ") != "mcp --profile work" {
			t.Fatalf("%s: profile install %+v", tc.harness, c)
		}
		updated, _ := os.ReadFile(tc.path)
		if !strings.Contains(string(updated), "--profile") {
			t.Fatalf("%s: profile not written\n%s", tc.harness, updated)
		}
	}

	// Entries this CLI does not own survive, comments included.
	var claudeDoc struct {
		NumStartups int `json:"numStartups"`
		MCPServers  map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	raw, _ := os.ReadFile(claude)
	decode(t, string(raw), &claudeDoc)
	if claudeDoc.NumStartups != 7 || claudeDoc.MCPServers["other"].Command != "other-mcp" {
		t.Fatalf("claude config lost keys: %s", raw)
	}
	if _, ok := claudeDoc.MCPServers["wallapop"]; !ok {
		t.Fatalf("claude config has no wallapop entry: %s", raw)
	}
	codexBody, _ := os.ReadFile(codex)
	for _, want := range []string{"# my codex config", `model = "gpt-5"`, "[mcp_servers.other]", "[mcp_servers.wallapop]"} {
		if !strings.Contains(string(codexBody), want) {
			t.Fatalf("codex config lost %q:\n%s", want, codexBody)
		}
	}
	if strings.Count(string(codexBody), "[mcp_servers.wallapop]") != 1 {
		t.Fatalf("codex entry duplicated:\n%s", codexBody)
	}
}

func TestMCPInstallKeepsSettingsItDoesNotOwn(t *testing.T) {
	h := newHarness(t)
	// An entry can carry env, timeouts or harness-specific keys the person put
	// there; only command and args belong to this CLI.
	cursor := filepath.Join(h.home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(cursor), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"mcpServers":{"wallapop":{"command":"old","args":["mcp"],"env":{"WALLAPOP_PROFILE":"work"},"timeout":60}}}`
	if err := os.WriteFile(cursor, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	h.must("", "mcp", "install", "cursor")

	var doc struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
			Timeout int
		} `json:"mcpServers"`
	}
	raw, _ := os.ReadFile(cursor)
	decode(t, string(raw), &doc)
	entry := doc.MCPServers["wallapop"]
	if entry.Command == "old" || entry.Env["WALLAPOP_PROFILE"] != "work" || entry.Timeout != 60 {
		t.Fatalf("install dropped settings it does not own: %s", raw)
	}
}

func TestMCPInstallKeepsCodexServerSettings(t *testing.T) {
	h := newHarness(t)
	codex := filepath.Join(h.home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	// codex keeps its own per-server settings in the same table.
	// The next table is indented, which TOML allows: the rewrite must stop at
	// it rather than reaching into another server's settings.
	body := "[mcp_servers.wallapop]\ncommand = \"old\"\n# how long codex waits\nstartup_timeout_sec = 30\nargs = [\n  \"mcp\",\n]\nenabled = true\n\n  [mcp_servers.other]\n  command = \"other-mcp\"\n"
	if err := os.WriteFile(codex, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h.must("", "mcp", "install", "codex")

	after, _ := os.ReadFile(codex)
	for _, want := range []string{"# how long codex waits", "startup_timeout_sec = 30", "enabled = true", "[mcp_servers.other]", "command = \"other-mcp\""} {
		if !strings.Contains(string(after), want) {
			t.Fatalf("install dropped %q:\n%s", want, after)
		}
	}
	if strings.Contains(string(after), `command = "old"`) || strings.Contains(string(after), "\"mcp\",\n") {
		t.Fatalf("the old command or args survived:\n%s", after)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
		} `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(after, &doc); err != nil {
		t.Fatalf("install left invalid toml: %v\n%s", err, after)
	}
	if doc.MCPServers["wallapop"].Command == "old" || strings.Join(doc.MCPServers["wallapop"].Args, " ") != "mcp" {
		t.Fatalf("entry not updated: %+v", doc.MCPServers["wallapop"])
	}
}

func TestMCPInstallFollowsASymlinkedConfig(t *testing.T) {
	h := newHarness(t)
	real := filepath.Join(h.home, "dotfiles", "cursor-mcp.json")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.home, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	h.must("", "mcp", "install", "cursor")

	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("install replaced the symlink with a file: %v", fi.Mode())
	}
	raw, _ := os.ReadFile(real)
	if !strings.Contains(string(raw), `"wallapop"`) {
		t.Fatalf("the symlink target was not updated: %s", raw)
	}
}

func TestMCPInstallRefusesATomlEntryItCannotRewrite(t *testing.T) {
	h := newHarness(t)
	codex := filepath.Join(h.home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	// A spelling the parser accepts but the rewrite does not recognise:
	// appending a second table would leave the file invalid.
	body := "[\"mcp_servers\".\"wallapop\"]\ncommand = \"old\"\nargs = [\"mcp\"]\n"
	if err := os.WriteFile(codex, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("", "mcp", "install", "codex")
	if r.code == 0 || !strings.Contains(r.stderr, "by hand") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	after, _ := os.ReadFile(codex)
	if string(after) != body {
		t.Fatalf("the config was touched anyway:\n%s", after)
	}
}

func TestMCPInstallRejectsAnUnknownHarness(t *testing.T) {
	h := newHarness(t)
	r := h.run("", "mcp", "install", "emacs")
	if r.code != 2 || !strings.Contains(r.stderr, "claude-code") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
}
