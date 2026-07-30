package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// runSession feeds the newline-delimited input through Serve and returns the
// decoded JSON-RPC responses (notifications produce no response).
func runSession(t *testing.T, s *Server, input string) []rpcResponse {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	var responses []rpcResponse
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var r rpcResponse
		if err := dec.Decode(&r); err != nil {
			break
		}
		responses = append(responses, r)
	}
	return responses
}

func echoTool() Tool {
	return Tool{
		Name:        "echo",
		Description: "echoes its input",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var v map[string]any
			if err := json.Unmarshal(args, &v); err != nil {
				return nil, err
			}
			return v, nil
		},
	}
}

func TestInitializeHandshake(t *testing.T) {
	s := NewServer("pgmi", "0.0.0-test")
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+ProtocolVersion+`"}}`)
	if len(resp) != 1 {
		t.Fatalf("expected 1 response, got %d", len(resp))
	}
	var res initializeResult
	mustRemarshal(t, resp[0].Result, &res)
	if res.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %q, want %q", res.ProtocolVersion, ProtocolVersion)
	}
	if res.ServerInfo.Name != "pgmi" || res.ServerInfo.Version != "0.0.0-test" {
		t.Errorf("serverInfo = %+v", res.ServerInfo)
	}
	if _, ok := res.Capabilities["tools"]; !ok {
		t.Errorf("capabilities missing tools: %+v", res.Capabilities)
	}
}

// Spec: "If the server supports the requested protocol version, it MUST respond
// with the same version. Otherwise, the server MUST respond with another protocol
// version it supports." An unsupported version is not an error response.
func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{"supported version is echoed back", "2025-06-18", "2025-06-18"},
		{"older supported version is echoed back", "2024-11-05", "2024-11-05"},
		{"unknown version falls back to our latest", "1.0.0", ProtocolVersion},
		{"absent version falls back to our latest", "", ProtocolVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewServer("pgmi", "v")
			req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + tt.requested + `"}}`
			resp := runSession(t, s, req)

			if resp[0].Error != nil {
				t.Fatalf("an unsupported version must not be a protocol error, got: %+v", resp[0].Error)
			}
			var res initializeResult
			mustRemarshal(t, resp[0].Result, &res)
			if res.ProtocolVersion != tt.want {
				t.Errorf("protocolVersion = %q, want %q", res.ProtocolVersion, tt.want)
			}
		})
	}
}

// MCP narrows JSON-RPC: the request id is `string | number` and "must not be
// null". A notification is a request with no id at all, so a null id is a
// malformed request, not a licence to stay silent.
func TestRequestIDClassification(t *testing.T) {
	tests := []struct {
		name      string
		request   string
		wantResp  bool
		wantError int
	}{
		{name: "absent id is a notification", request: `{"jsonrpc":"2.0","method":"ping"}`},
		{name: "null id is an invalid request", request: `{"jsonrpc":"2.0","id":null,"method":"ping"}`, wantResp: true, wantError: codeInvalidRequest},
		{name: "boolean id is an invalid request", request: `{"jsonrpc":"2.0","id":true,"method":"ping"}`, wantResp: true, wantError: codeInvalidRequest},
		{name: "object id is an invalid request", request: `{"jsonrpc":"2.0","id":{},"method":"ping"}`, wantResp: true, wantError: codeInvalidRequest},
		{name: "numeric id is answered", request: `{"jsonrpc":"2.0","id":1,"method":"ping"}`, wantResp: true},
		{name: "string id is answered", request: `{"jsonrpc":"2.0","id":"a","method":"ping"}`, wantResp: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := runSession(t, NewServer("pgmi", "v"), tt.request)

			if !tt.wantResp {
				if len(resp) != 0 {
					t.Fatalf("expected no response, got %d: %+v", len(resp), resp)
				}
				return
			}
			if len(resp) != 1 {
				t.Fatalf("expected 1 response, got %d", len(resp))
			}

			if tt.wantError == 0 {
				if resp[0].Error != nil {
					t.Fatalf("expected a result, got error %+v", resp[0].Error)
				}
				return
			}
			if resp[0].Error == nil {
				t.Fatalf("expected error %d, got result %+v", tt.wantError, resp[0].Result)
			}
			if resp[0].Error.Code != tt.wantError {
				t.Errorf("error code = %d, want %d", resp[0].Error.Code, tt.wantError)
			}
		})
	}
}

// The SQL MCP surface rejects a handshake with no protocolVersion (-32602,
// api.mcp_initialize). pgmi serve must answer the same way, or one product
// ships two servers that disagree about what a valid handshake is. Matching it
// exactly means an absent key is an error while an empty value negotiates —
// `p_params->>'protocolVersion'` is NULL only when the key is missing.
func TestInitializeRequiresProtocolVersion(t *testing.T) {
	tests := []struct {
		name      string
		params    string
		wantError int
	}{
		{name: "no params at all", params: "", wantError: codeInvalidParams},
		{name: "empty params object", params: `,"params":{}`, wantError: codeInvalidParams},
		{name: "malformed params", params: `,"params":"not-an-object"`, wantError: codeInvalidParams},
		{name: "version present", params: `,"params":{"protocolVersion":"2025-06-18"}`},
		{name: "version present but empty negotiates", params: `,"params":{"protocolVersion":""}`},
		{name: "capabilities and clientInfo stay optional", params: `,"params":{"protocolVersion":"2025-06-18"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewServer("pgmi", "v")
			resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize"`+tt.params+`}`)
			if len(resp) != 1 {
				t.Fatalf("expected 1 response, got %d", len(resp))
			}

			if tt.wantError == 0 {
				if resp[0].Error != nil {
					t.Fatalf("expected a successful handshake, got %+v", resp[0].Error)
				}
				return
			}
			if resp[0].Error == nil {
				t.Fatalf("expected error %d, got result %+v", tt.wantError, resp[0].Result)
			}
			if resp[0].Error.Code != tt.wantError {
				t.Errorf("error code = %d, want %d", resp[0].Error.Code, tt.wantError)
			}
		})
	}
}

func TestToolsListDeclaresOutputSchemaOnlyWhenSet(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{Name: "text_tool", Description: "returns markdown"})
	s.Register(Tool{
		Name:         "structured_tool",
		Description:  "returns an object",
		OutputSchema: map[string]any{"type": "object"},
	})

	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var res toolsListResult
	mustRemarshal(t, resp[0].Result, &res)

	byName := map[string]toolDescriptor{}
	for _, d := range res.Tools {
		byName[d.Name] = d
	}

	if byName["structured_tool"].OutputSchema == nil {
		t.Error("a tool with an output schema must advertise it")
	}
	// A tool returning plain text produces no structuredContent, so advertising a
	// schema for it would promise output the tool never sends.
	if byName["text_tool"].OutputSchema != nil {
		t.Error("a tool without an output schema must omit the field entirely")
	}
}

func TestToolsListSortedAndRegistered(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{Name: "zebra", Description: "z"})
	s.Register(echoTool())
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var res toolsListResult
	mustRemarshal(t, resp[0].Result, &res)
	if len(res.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(res.Tools))
	}
	if res.Tools[0].Name != "echo" || res.Tools[1].Name != "zebra" {
		t.Errorf("tools not sorted: %v, %v", res.Tools[0].Name, res.Tools[1].Name)
	}
	if res.Tools[0].InputSchema == nil {
		t.Errorf("echo missing inputSchema")
	}
}

func TestRegisterDefaultsInputSchema(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{Name: "noschema", Description: "d"})
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var res toolsListResult
	mustRemarshal(t, resp[0].Result, &res)
	if res.Tools[0].InputSchema["type"] != "object" {
		t.Errorf("default inputSchema not applied: %+v", res.Tools[0].InputSchema)
	}
}

func TestToolsCallSuccess(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(echoTool())
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"hi":"there"}}}`)
	var res callToolResult
	mustRemarshal(t, resp[0].Result, &res)
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("unexpected content: %+v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, `"hi": "there"`) {
		t.Errorf("text content missing echoed value: %q", res.Content[0].Text)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if sc["hi"] != "there" {
		t.Errorf("structuredContent missing echoed value: %+v", res.StructuredContent)
	}
}

func TestToolsCallHandlerErrorIsErrorResult(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{
		Name: "boom",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("kaboom")
		},
	})
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	var res callToolResult
	mustRemarshal(t, resp[0].Result, &res)
	if !res.IsError {
		t.Fatalf("expected isError, got %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "kaboom") {
		t.Errorf("error text = %q", res.Content[0].Text)
	}
	// A tool failure is a result, not a protocol error.
	if resp[0].Error != nil {
		t.Errorf("expected no protocol error, got %+v", resp[0].Error)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	s := NewServer("pgmi", "v")
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`)
	if resp[0].Error == nil || resp[0].Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params error, got %+v", resp[0])
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	s := NewServer("pgmi", "v")
	resp := runSession(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(resp) != 0 {
		t.Fatalf("notification should produce no response, got %d", len(resp))
	}
}

func TestNotificationProducesNoResponse_AllMethods(t *testing.T) {
	s := NewServer("pgmi", "v")
	called := false
	s.Register(Tool{
		Name:        "spy",
		Description: "records whether it was called",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			called = true
			return "ok", nil
		},
	})

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"initialize", `{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2025-06-18"}}`},
		{"tools/list", `{"jsonrpc":"2.0","method":"tools/list"}`},
		{"tools/call", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"spy","arguments":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			resp := runSession(t, s, tc.input)
			if len(resp) != 0 {
				t.Fatalf("notification %q should produce no response, got %d", tc.name, len(resp))
			}
			if tc.name == "tools/call" && called {
				t.Fatal("tools/call notification must not execute the tool")
			}
		})
	}
}

func TestMethodNotFound(t *testing.T) {
	s := NewServer("pgmi", "v")
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"does/not/exist"}`)
	if resp[0].Error == nil || resp[0].Error.Code != codeMethodNotFound {
		t.Fatalf("expected method-not-found, got %+v", resp[0])
	}
}

func TestPing(t *testing.T) {
	s := NewServer("pgmi", "v")
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	if resp[0].Error != nil {
		t.Fatalf("ping errored: %+v", resp[0].Error)
	}
}

func TestFullSessionMultipleMessages(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(echoTool())
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`,
	}, "\n")
	resp := runSession(t, s, input)
	// initialize, tools/list, tools/call → 3 responses (notification suppressed).
	if len(resp) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(resp))
	}
}

func TestContextCancellationStops(t *testing.T) {
	s := NewServer("pgmi", "v")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out strings.Builder
	err := s.Serve(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestContextCancellationUnblocksRead(t *testing.T) {
	s := NewServer("pgmi", "v")
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	var out strings.Builder

	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, pr, &out) }()

	_, _ = pw.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"))
	time.Sleep(50 * time.Millisecond)

	cancel()
	pw.CloseWithError(errors.New("file already closed"))

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3s after context cancellation")
	}

	outStr := out.String()
	if strings.Contains(outStr, "parse error") {
		t.Errorf("shutdown should not emit a parse-error frame, got %q", outStr)
	}
}

func mustRemarshal(t *testing.T, from any, to any) {
	t.Helper()
	b, err := json.Marshal(from)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, to); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestToolsCallErrorStructuredContent(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{
		Name: "boom",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, &FieldsError{
				Err:    errors.New("kaboom"),
				Fields: map[string]any{"notices": []string{"[pgmi] Test: ./__test__/x.sql"}},
			}
		},
	})
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	var res callToolResult
	mustRemarshal(t, resp[0].Result, &res)
	if !res.IsError {
		t.Fatalf("expected isError, got %+v", res)
	}
	sc, _ := res.StructuredContent.(map[string]any)
	if sc["message"] != "kaboom" {
		t.Errorf("structuredContent message = %v", sc["message"])
	}
	if _, ok := sc["exitCode"]; !ok {
		t.Errorf("structuredContent missing exitCode: %+v", sc)
	}
	notices, _ := sc["notices"].([]any)
	if len(notices) != 1 || notices[0] != "[pgmi] Test: ./__test__/x.sql" {
		t.Errorf("notices = %+v", sc["notices"])
	}
}

// rawSession returns exactly what Serve wrote. The decoded form cannot see the
// difference between an absent id and a null one, which is the whole defect.
func rawSession(t *testing.T, s *Server, input string) string {
	t.Helper()
	var out strings.Builder
	if err := s.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	return out.String()
}

// TestServeParseError pins JSON-RPC 2.0 §5: a response always carries id, and
// null when the id could not be determined. omitempty dropped the key entirely,
// and a strict client rejects that response as malformed.
func TestServeParseError(t *testing.T) {
	s := NewServer("pgmi", "v")
	raw := rawSession(t, s, "{\"jsonrpc\":\"2.0\"\n")

	if !strings.Contains(raw, `"id":null`) {
		t.Errorf("parse error response must carry an explicit null id, got:\n%s", raw)
	}

	var resp rpcResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, raw)
	}
	if resp.Error == nil || resp.Error.Code != codeParseError {
		t.Fatalf("expected a parse error, got %+v", resp)
	}
}

// Every response carries id, not just the parse-error one.
func TestEveryResponseCarriesAnID(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(echoTool())

	raw := rawSession(t, s, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nosuch"}`,
	}, "\n"))

	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response is not valid JSON: %v\n%s", err, line)
		}
		if _, ok := m["id"]; !ok {
			t.Errorf("response omits the required id member: %s", line)
		}
	}
}

// A parse error is per-message, not per-session: the stdio transport delimits
// messages by newlines, so the next newline is a known-good boundary and the
// server keeps serving. It used to return instead — one truncated frame from a
// buggy client killed the session, and because Serve returned nil the client
// saw a clean EOF rather than a failure.
func TestServeRecoversAfterParseError(t *testing.T) {
	s := NewServer("pgmi", "v")
	raw := rawSession(t, s, "{\"jsonrpc\":\"2.0\"\n"+`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")

	if !strings.Contains(raw, `"code":-32700`) {
		t.Errorf("no parse error reported for the malformed frame:\n%s", raw)
	}
	if !strings.Contains(raw, `"id":2`) {
		t.Errorf("the request after a parse error went unanswered — one bad frame "+
			"still ends the session:\n%s", raw)
	}
}

// An id that is not a string or number is an Invalid Request, and JSON-RPC 2.0
// requires the response id to be Null when the request's id could not be
// detected. Echoing the offending value back hands the client the very thing it
// was told was malformed, and no client can match it to a request.
//
// The same battery runs against the advanced template's SQL gateway
// (api.mcp_handle_request); the two MCP surfaces must agree on what a valid
// request is. The SQL side used to answer all four of these with a result.
func TestServeRejectsMalformedRequestIDWithNullID(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"null", "null"},
		{"boolean", "true"},
		{"object", `{"a":1}`},
		{"array", "[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := rawSession(t, NewServer("pgmi", "v"),
				`{"jsonrpc":"2.0","id":`+tc.id+`,"method":"ping"}`+"\n")

			if !strings.Contains(raw, `"code":-32600`) {
				t.Errorf("id %s was not rejected as an Invalid Request: %s", tc.id, raw)
			}
			if !strings.Contains(raw, `"id":null`) {
				t.Errorf("id %s: response must carry a null id, got: %s", tc.id, raw)
			}
			if strings.Contains(raw, `"result"`) {
				t.Errorf("id %s produced a result: %s", tc.id, raw)
			}
		})
	}
}

// A well-formed id is still echoed unchanged — the nulling above must not leak
// into the normal path.
func TestServeEchoesWellFormedIDs(t *testing.T) {
	for _, id := range []string{`"abc"`, `7`, `1.5`} {
		raw := rawSession(t, NewServer("pgmi", "v"),
			`{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`+"\n")
		if !strings.Contains(raw, `"id":`+id) {
			t.Errorf("id %s was not echoed back: %s", id, raw)
		}
	}
}

// The spec forbids embedded newlines in a message, so a JSON value split across
// lines is not one message: each line is framed separately and neither half
// parses. The point is that the server reports and keeps going rather than
// silently accepting a frame the transport disallows.
func TestServeRejectsMultiLineMessageAndContinues(t *testing.T) {
	s := NewServer("pgmi", "v")
	raw := rawSession(t, s,
		"{\"jsonrpc\":\"2.0\",\n\"id\":1,\"method\":\"ping\"}\n"+
			`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")

	if !strings.Contains(raw, `"id":2`) {
		t.Errorf("the well-formed request after a split frame went unanswered:\n%s", raw)
	}
}

// Blank lines carry no message. json.Decoder skipped inter-value whitespace
// silently, so preserving that keeps a client that pads its writes working
// instead of drowning it in -32700.
func TestServeSkipsBlankLines(t *testing.T) {
	s := NewServer("pgmi", "v")
	raw := rawSession(t, s, "\n\n   \n"+`{"jsonrpc":"2.0","id":7,"method":"ping"}`+"\n\n")

	if strings.Contains(raw, "-32700") {
		t.Errorf("a blank line was reported as a parse error:\n%s", raw)
	}
	if !strings.Contains(raw, `"id":7`) {
		t.Errorf("request surrounded by blank lines went unanswered:\n%s", raw)
	}
}

// A final message need not be newline-terminated. Reading by line makes the
// last read return data together with io.EOF, which is easy to drop.
func TestServeHandlesFinalMessageWithoutNewline(t *testing.T) {
	s := NewServer("pgmi", "v")
	raw := rawSession(t, s, `{"jsonrpc":"2.0","id":9,"method":"ping"}`)

	if !strings.Contains(raw, `"id":9`) {
		t.Errorf("unterminated final message went unanswered:\n%s", raw)
	}
}

// bufio.Scanner would cap a line at 64 KiB and report the overflow as
// end-of-input, ending the session on a large but valid frame — the reason
// Serve reads with bufio.Reader instead. The argument here is ~256 KiB.
func TestServeAcceptsFrameLargerThanScannerLimit(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{
		Name: "echo_len",
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			return map[string]any{"n": len(args)}, nil
		},
	})

	big, _ := json.Marshal(strings.Repeat("x", 256*1024))
	raw := rawSession(t, s, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":`+
		`{"name":"echo_len","arguments":{"blob":`+string(big)+`}}}`+"\n"+
		`{"jsonrpc":"2.0","id":12,"method":"ping"}`+"\n")

	if !strings.Contains(raw, `"id":11`) {
		t.Errorf("a 256 KiB frame went unanswered — the reader is capping lines:\n%s",
			truncateForLog(raw))
	}
	if !strings.Contains(raw, `"id":12`) {
		t.Errorf("the session did not survive a large frame:\n%s", truncateForLog(raw))
	}
}

func truncateForLog(s string) string {
	if len(s) > 400 {
		return s[:400] + "...(truncated)"
	}
	return s
}

// TestToolsCallHandlerPanicIsErrorResult: a panicking handler used to unwind
// through Serve to main, which printed a stack trace and exited 3. The client
// saw stdout EOF mid-session and lost the server for the rest of the
// conversation.
func TestToolsCallHandlerPanicIsErrorResult(t *testing.T) {
	s := NewServer("pgmi", "v")
	s.Register(Tool{
		Name: "panics",
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			// A handler indexing a result set it never checked — the shape of
			// bug that actually reaches this path. The index is dynamic so the
			// panic is a real runtime error, not one the compiler folds away.
			var rows []int
			return rows[len(args)], nil
		},
	})

	resp := runSession(t, s, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"panics","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	}, "\n"))

	if len(resp) != 2 {
		t.Fatalf("expected 2 responses — the session must survive the panic — got %d", len(resp))
	}

	var res callToolResult
	mustRemarshal(t, resp[0].Result, &res)
	if !res.IsError {
		t.Fatalf("a panicking handler must produce isError, got %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, `tool "panics" panicked`) {
		t.Errorf("the error text does not name the panicking tool: %q", res.Content[0].Text)
	}
	// A tool failure is a result, not a protocol error — same as a returned error.
	if resp[0].Error != nil {
		t.Errorf("expected no protocol error, got %+v", resp[0].Error)
	}

	// The stack is what makes a recovered panic diagnosable at all.
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent is %T, want an object", res.StructuredContent)
	}
	if sc["panic"] != true {
		t.Errorf("structuredContent does not flag the panic: %v", sc)
	}
	if stack, _ := sc["stack"].(string); !strings.Contains(stack, "internal/mcp") {
		t.Errorf("structuredContent carries no usable stack: %q", stack)
	}

	// The second request still got an answer.
	if string(resp[1].ID) != "2" {
		t.Errorf("the request after the panic was not answered; got id %s", resp[1].ID)
	}
}
