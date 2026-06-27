package mcp

import (
	"context"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Tool is a single MCP tool: a name, a human description, a JSON Schema for its
// arguments, and a handler that maps decoded arguments to a result value. The
// returned value is JSON-encoded into the tool result; a returned error becomes
// an MCP error result (isError: true) rather than a protocol-level error.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args json.RawMessage) (any, error)
}

// Server dispatches JSON-RPC 2.0 / MCP messages to registered tools.
type Server struct {
	info  serverInfo
	tools map[string]Tool
}

// NewServer creates a server advertising the given name and version.
func NewServer(name, version string) *Server {
	return &Server{
		info:  serverInfo{Name: name, Version: version},
		tools: make(map[string]Tool),
	}
}

// Register adds a tool. A later registration with the same name replaces the
// earlier one.
func (s *Server) Register(t Tool) {
	if t.InputSchema == nil {
		t.InputSchema = map[string]any{"type": "object"}
	}
	s.tools[t.Name] = t
}

// Serve reads newline-delimited JSON-RPC messages from in, dispatches each, and
// writes responses to out. It returns nil on clean EOF and ctx.Err() if the
// context is cancelled (e.g. SIGINT). Notifications produce no response.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		var req rpcRequest
		err := dec.Decode(&req)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// Malformed JSON: report a parse error with a null id and continue.
			if writeErr := s.write(enc, nil, nil, &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}); writeErr != nil {
				return writeErr
			}
			// A decode error leaves the stream position undefined; stop reading.
			return nil
		}

		resp, isNotification := s.dispatch(ctx, req)
		if isNotification {
			continue
		}
		if err := s.write(enc, req.ID, resp.Result, resp.Error); err != nil {
			return err
		}
	}
}

func (s *Server) write(enc *json.Encoder, id json.RawMessage, result any, rpcErr *rpcError) error {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr}
	if err := enc.Encode(resp); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

// dispatch routes a request to its handler. The second return reports whether
// the request was a notification (no response should be written).
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if req.JSONRPC != "2.0" {
		return rpcResponse{Error: &rpcError{Code: codeInvalidRequest, Message: "jsonrpc must be \"2.0\""}}, req.isNotification()
	}

	switch req.Method {
	case "initialize":
		return rpcResponse{Result: s.handleInitialize()}, false
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{}, true
	case "ping":
		return rpcResponse{Result: map[string]any{}}, req.isNotification()
	case "tools/list":
		return rpcResponse{Result: s.handleToolsList()}, false
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params), false
	default:
		if req.isNotification() {
			return rpcResponse{}, true
		}
		return rpcResponse{Error: &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}}, false
	}
}

func (s *Server) handleInitialize() initializeResult {
	return initializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      s.info,
	}
}

func (s *Server) handleToolsList() toolsListResult {
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	slices.SortFunc(names, cmp.Compare)
	tools := make([]toolDescriptor, 0, len(names))
	for _, name := range names {
		t := s.tools[name]
		tools = append(tools, toolDescriptor{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return toolsListResult{Tools: tools}
}

func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) rpcResponse {
	var p callToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return rpcResponse{Error: &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params: " + err.Error()}}
	}

	tool, ok := s.tools[p.Name]
	if !ok {
		return rpcResponse{Error: &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}}
	}

	result, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return rpcResponse{Result: errorResult(err)}
	}
	return rpcResponse{Result: successResult(result)}
}

// successResult renders a tool's return value as text content. String results
// (markdown skills, already-JSON contracts) are emitted verbatim; everything
// else is pretty-printed JSON and mirrored into structuredContent.
func successResult(result any) callToolResult {
	if s, ok := result.(string); ok {
		return callToolResult{Content: []contentBlock{{Type: "text", Text: s}}}
	}
	text, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return errorResult(fmt.Errorf("encode result: %w", err))
	}
	return callToolResult{
		Content:           []contentBlock{{Type: "text", Text: string(text)}},
		StructuredContent: result,
	}
}

// errorResult reports a tool failure to the client as an MCP error result.
func errorResult(err error) callToolResult {
	return callToolResult{
		Content: []contentBlock{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}
