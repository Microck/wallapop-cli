// Package mcp serves the CLI's read and message commands to an agent harness
// over the Model Context Protocol, stdio transport.
//
// The transport is newline-delimited JSON-RPC 2.0, which is all the stdio
// binding is, so it is written out here rather than pulled in as a dependency:
// the same reason the rest of the repo speaks to PubNub over net/http.
//
// Every tool is defined as a CLI argv, never as a second call into the API
// client. The MCP output is therefore the same JSON the command prints, by
// construction, and cannot drift from it.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// protocolVersions are the MCP revisions this server answers to. A client
// asking for one of them gets it back; anything else gets the newest.
var protocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

// Result is one CLI invocation's outcome as a tool reports it.
type Result struct {
	// Stdout is what the command printed, already in the CLI's JSON shape.
	Stdout string
	// ErrorJSON is the error envelope the CLI prints with --error-format json,
	// empty when the command succeeded. It carries the exit code, so an agent
	// reads the same categories a shell would.
	ErrorJSON string
}

// Runner executes one CLI invocation in the host process.
type Runner func(ctx context.Context, argv []string) Result

// Server answers MCP requests on one stdio pair.
type Server struct {
	Version string
	Run     Runner
}

// Serve reads requests until in is exhausted. Responses go to out, one JSON
// object per line; nothing else may be written there. Cancelling ctx stops the
// loop at the next message boundary, so a harness that wants an immediate stop
// closes stdin (or kills the process), as harnesses do.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	// Tool arguments stay small, but a pasted message body should not kill the
	// session at the 64 KiB scanner default.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		resp, ok := s.handle(ctx, []byte(line))
		if !ok {
			continue // notification: no reply
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// handle answers one message. The second return is false for notifications,
// which by JSON-RPC rule get no response at all.
func (s *Server) handle(ctx context.Context, line []byte) (response, bool) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return errorResponse(nil, codeParse, "invalid json"), true
	}
	if req.Method == "" {
		return errorResponse(req.ID, codeInvalidRequest, "missing method"), true
	}
	if len(req.ID) == 0 {
		return response{}, false
	}

	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", ID: req.ID, Result: s.initialize(req.Params)}, true
	case "ping":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, true
	case "tools/list":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": listTools()}}, true
	case "tools/call":
		result, rerr := s.call(ctx, req.Params)
		if rerr != nil {
			return response{JSONRPC: "2.0", ID: req.ID, Error: rerr}, true
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: result}, true
	}
	return errorResponse(req.ID, codeMethodNotFound, "unknown method "+req.Method), true
}

func errorResponse(id json.RawMessage, code int, msg string) response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := protocolVersions[len(protocolVersions)-1]
	for _, v := range protocolVersions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "wallapop", "version": s.Version},
		"instructions": "Wallapop through the wallapop CLI. Tool output is the JSON the matching command prints; " +
			"a command that fails comes back as the CLI's error envelope, exit code included. " +
			"Selling actions (reserve, sold, delete, create, edit) are deliberately not tools.",
	}
}

// call runs one tool. A protocol-level fault (unknown tool, bad argument
// types) is a JSON-RPC error; a command that ran and failed is a tool error,
// so the agent can read the envelope and retry.
func (s *Server) call(ctx context.Context, params json.RawMessage) (map[string]any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid params"}
	}
	tool, ok := lookupTool(p.Name)
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: unknownToolMessage(p.Name)}
	}
	argv, err := tool.argv(p.Arguments)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	out := s.Run(ctx, argv)
	if out.ErrorJSON != "" {
		return textResult(out.ErrorJSON, true), nil
	}
	return textResult(out.Stdout, false), nil
}

func textResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": strings.TrimRight(text, "\n")}},
		"isError": isError,
	}
}

// unknownToolMessage names the withheld tools explicitly. An agent that asks
// for `item_sold` should learn it will never exist, not that it misspelled it.
func unknownToolMessage(name string) string {
	for _, w := range withheld {
		if w.name == name {
			return fmt.Sprintf("tool %q is not offered: %s", name, w.why)
		}
	}
	if !chatToolsEnabled {
		for _, t := range chatTools {
			if t.Name == name {
				return fmt.Sprintf("tool %q is not offered yet: live message receive is still unverified (issue #3). Use `wallapop %s` at the terminal", name, strings.ReplaceAll(name, "_", " "))
			}
		}
	}
	names := make([]string, 0, len(listTools()))
	for _, t := range listTools() {
		names = append(names, t.Name)
	}
	return fmt.Sprintf("unknown tool %q. Available: %s", name, strings.Join(names, ", "))
}
