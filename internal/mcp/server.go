package mcp

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Tool is a single MCP tool: a name, a human description, a JSON Schema for its
// arguments, and a handler that maps decoded arguments to a result value. The
// returned value is JSON-encoded into the tool result; a returned error becomes
// an MCP error result (isError: true) rather than a protocol-level error.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// OutputSchema describes the tool's structuredContent. Declare it only on
	// tools that return a structured value: the spec requires a tool advertising
	// an output schema to produce conforming structuredContent, and a tool
	// returning plain text (a skill, a markdown overview) produces none.
	OutputSchema map[string]any
	Handler      func(ctx context.Context, args json.RawMessage) (any, error)
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
//
// Framing is per line because that is what the stdio transport defines:
// "Messages are delimited by newlines, and MUST NOT contain embedded newlines."
// Reading with json.Decoder instead treated the stream as a run of concatenated
// values, which got both directions wrong — it accepted the multi-line messages
// the spec forbids, and could not resynchronise after a syntax error (buffer
// position undefined), so one truncated frame from a buggy client ended the
// session and the client had to restart the server.
//
// bufio.Reader.ReadString, not bufio.Scanner: Scanner caps a line at 64 KiB by
// default and reports the overflow as end-of-input, which would end the session
// on a large *valid* frame — a worse failure than the one being fixed.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	rd := bufio.NewReader(in)
	enc := json.NewEncoder(out)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		line, readErr := rd.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return readErr
		}
		atEOF := errors.Is(readErr, io.EOF)

		// A final message need not be newline-terminated, so a non-empty read
		// is dispatched even when it arrived with EOF.
		if strings.TrimSpace(line) == "" {
			if atEOF {
				return nil
			}
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// Per-message, per JSON-RPC 2.0: report the parse error with a null
			// id and keep reading. The next newline is a known-good boundary.
			if writeErr := s.write(enc, nil, nil, &rpcError{
				Code:    codeParseError,
				Message: "parse error: " + err.Error(),
			}); writeErr != nil {
				return writeErr
			}
			if atEOF {
				return nil
			}
			continue
		}

		resp, isNotification := s.dispatch(ctx, req)
		if !isNotification {
			// JSON-RPC 2.0: "If there was an error in detecting the id in the
			// Request object (e.g. Parse error/Invalid Request), it MUST be
			// Null." hasWellFormedID is false exactly in that case, so echoing
			// req.ID there would hand back the malformed value it rejected.
			id := req.ID
			if !req.hasWellFormedID() {
				id = nil
			}
			if err := s.write(enc, id, resp.Result, resp.Error); err != nil {
				return err
			}
		}
		if atEOF {
			return nil
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
	if !req.isNotification() && !req.hasWellFormedID() {
		return rpcResponse{Error: &rpcError{Code: codeInvalidRequest, Message: "id must be a string or number"}}, false
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params), req.isNotification()
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{}, true
	case "ping":
		return rpcResponse{Result: map[string]any{}}, req.isNotification()
	case "tools/list":
		return rpcResponse{Result: s.handleToolsList()}, req.isNotification()
	case "tools/call":
		if req.isNotification() {
			return rpcResponse{}, true
		}
		return s.handleToolsCall(ctx, req.Params), false
	default:
		if req.isNotification() {
			return rpcResponse{}, true
		}
		return rpcResponse{Error: &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}}, false
	}
}

// handleInitialize negotiates the protocol version. protocolVersion is required
// — api.mcp_initialize rejects a handshake without it and the two surfaces must
// agree. capabilities and clientInfo stay optional: the spec requires clients to
// send them, but refusing a client that omits them breaks working clients for no
// gain. An unknown version is still negotiated, not rejected.
func (s *Server) handleInitialize(params json.RawMessage) rpcResponse {
	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return rpcResponse{Error: &rpcError{Code: codeInvalidParams, Message: "invalid initialize params: " + err.Error()}}
		}
	}
	if p.ProtocolVersion == nil {
		return rpcResponse{Error: &rpcError{Code: codeInvalidParams, Message: "missing required parameter: protocolVersion"}}
	}
	return rpcResponse{Result: initializeResult{
		ProtocolVersion: negotiateVersion(*p.ProtocolVersion),
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      s.info,
	}}
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
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
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

	result, err := callTool(ctx, tool, p.Arguments)
	if err != nil {
		return rpcResponse{Result: errorResult(err)}
	}
	return rpcResponse{Result: successResult(result)}
}

// callTool turns a panicking handler into an ordinary error result. Without the
// recover the panic unwinds through Serve to main, which prints a stack trace
// and exits 3: the client sees stdout EOF mid-session and loses the pgmi server
// for the rest of the conversation, instead of getting isError: true and
// carrying on. The stack rides along in structuredContent — a recovered panic
// whose stack is gone is barely more diagnosable than the crash it replaced.
func callTool(ctx context.Context, t Tool, args json.RawMessage) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &FieldsError{
				Err: fmt.Errorf("tool %q panicked: %v", t.Name, r),
				Fields: map[string]any{
					"panic": true,
					"stack": string(debug.Stack()),
				},
			}
		}
	}()
	return t.Handler(ctx, args)
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

// FieldsError lets a tool attach extra structured fields (e.g. the notice
// stream of a failed deploy) to its MCP error result.
type FieldsError struct {
	Err    error
	Fields map[string]any
}

func (e *FieldsError) Error() string { return e.Err.Error() }
func (e *FieldsError) Unwrap() error { return e.Err }

// errorResult reports a tool failure to the client as an MCP error result.
// The text carries the DETAIL/HINT/WHERE diagnostics psql would show; the
// structured form lets the client branch on sqlstate/exitCode without parsing.
func errorResult(err error) callToolResult {
	var sc any = pgmi.NewErrorDetail(err)
	var fe *FieldsError
	if errors.As(err, &fe) && len(fe.Fields) > 0 {
		m := structToMap(sc)
		maps.Copy(m, fe.Fields)
		sc = m
	}
	return callToolResult{
		Content:           []contentBlock{{Type: "text", Text: pgmi.FormatError(err)}},
		StructuredContent: sc,
		IsError:           true,
	}
}

func structToMap(v any) map[string]any {
	m := map[string]any{}
	b, err := json.Marshal(v)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}
