// Package mcp implements a minimal Model Context Protocol server over stdio.
// It exposes registered tools to MCP-capable clients (Claude Code, OpenCode)
// via JSON-RPC 2.0, framed as newline-delimited JSON.
package mcp

import "encoding/json"

// SupportedVersions are the MCP revisions this server speaks, newest first.
// tools/list and tools/call are unchanged across these; structuredContent is
// additive in 2025-06-18 and ignored by older clients.
var SupportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// ProtocolVersion is the newest revision this server speaks — what it answers
// with when the client asks for something it does not know.
const ProtocolVersion = "2025-06-18"

// negotiateVersion implements the spec's rule: echo the client's version when we
// support it, otherwise answer with our latest. An unknown version is not an
// error — the client decides whether our answer is acceptable and disconnects if
// not.
func negotiateVersion(requested string) string {
	for _, v := range SupportedVersions {
		if v == requested {
			return v
		}
	}
	return ProtocolVersion
}

// JSON-RPC 2.0 error codes (subset used by this server).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

// isNotification reports whether a request carries no id (JSON-RPC notification).
func (r rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

// toolDescriptor is the public shape returned by tools/list. OutputSchema is
// present only for tools that return structuredContent — the spec requires a
// tool declaring one to actually produce conforming structured output.
type toolDescriptor struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
}

type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// callToolResult is the MCP tools/call response. Text content carries the
// JSON-encoded tool result; structuredContent repeats it for clients that
// consume typed output.
type callToolResult struct {
	Content           []contentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
