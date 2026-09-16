package mcp

import (
	"context"
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
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve stayed blocked on stdin after the context was cancelled")
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
