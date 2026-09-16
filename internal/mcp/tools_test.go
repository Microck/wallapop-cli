package mcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// A harness may stop the server with a signal while stdin is still open, which
// is what the cancelled context stands for here. The pipe is never written to,
// so the read is blocked when the cancel lands.
func TestServeReturnsWhenTheContextIsCancelledOnIdleStdin(t *testing.T) {
	in, hold := io.Pipe()
	defer hold.Close()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{Version: "test"}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, in, io.Discard) }()
	cancel()
	select {
	case err := <-done:
		// The cancellation is reported, not swallowed: the CLI turns it into
		// exit 130.
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve stayed blocked on stdin after the context was cancelled")
	}
}

// stripOwnedKeys decides what survives an update of a codex table, so the
// spellings TOML allows are checked here rather than through one install per
// case. Dropping a key it should keep, or keeping one it should drop, both
// leave the file unparseable for codex.
func TestStripOwnedKeysLeavesEverythingElseAlone(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"quoted keys", "\"command\" = \"old\"\n'args' = [\"mcp\"]\nenabled = true\n", "enabled = true\n"},
		{"multi-line array", "args = [\n  \"mcp\",\n  \"--profile\", \"work\",\n]\nenabled = true\n", "enabled = true\n"},
		{"bracket inside a string", "command = \"/opt/we[ird/wallapop\"\nstartup_timeout_sec = 30\n", "startup_timeout_sec = 30\n"},
		{"comment after the value", "args = [\"mcp\"] # set by the installer\nenabled = true\n", "enabled = true\n"},
		{"nothing owned", "enabled = true\n# a note\n", "enabled = true\n# a note\n"},
	}
	for _, tc := range cases {
		if got := stripOwnedKeys(tc.body); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// tableEnd has to tell a header from a `[` that only looks like one, which is
// why it asks the parser rather than the regex alone.
func TestTableEndStopsAtRealHeaders(t *testing.T) {
	body := "[mcp_servers.wallapop]\nargs = [\"\"\"\n[profile]\n\"\"\"]\nenabled = true\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	loc := tomlTable.FindStringIndex(body)
	if loc == nil {
		t.Fatal("the owned header did not match")
	}
	end := tableEnd(body, loc[0], loc[1])
	if rest := body[end:]; rest != "[mcp_servers.other]\ncommand = \"other\"\n" {
		t.Fatalf("the table ended at the wrong place, rest was %q", rest)
	}
}

// The server itself is tested through the stdio transport in internal/cli.
// What is left here is the argv mapping of the chat tools: they are written
// but gated (see chatToolsEnabled), so the transport tests cannot reach them
// and this is the only place their command lines are checked.
func TestChatToolsMapToTheirCommands(t *testing.T) {
	cases := []struct {
		tool Tool
		in   map[string]any
		want string
	}{
		{chatListTool, map[string]any{"unread": true, "limit": float64(5)}, "chat list --unread --limit 5"},
		{chatListTool, map[string]any{"unread": false}, "chat list"},
		{chatShowTool, map[string]any{"conversation": "8f1c2", "no_mark_read": true}, "chat show --no-mark-read -- 8f1c2"},
		{chatSendTool, map[string]any{"conversation": "8f1c2", "text": "sigue disponible?"}, "chat send -- 8f1c2 sigue disponible?"},
		{chatStartTool, map[string]any{"item": "k2j3h4g5f6d7", "text": "hola"}, "chat start -- k2j3h4g5f6d7 hola"},
	}
	for _, tc := range cases {
		argv, err := tc.tool.argv(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.tool.Name, err)
		}
		if got := strings.Join(argv, " "); got != tc.want {
			t.Errorf("%s: argv %q, want %q", tc.tool.Name, got, tc.want)
		}
	}
	if _, err := chatSendTool.argv(map[string]any{"conversation": "8f1c2"}); err == nil {
		t.Error("chat_send without text should be refused")
	}
	if _, err := chatSendTool.argv(map[string]any{"conversation": "8f1c2", "text": ""}); err == nil {
		t.Error("chat_send with an empty message should be refused")
	}
}
