package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
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
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
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
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
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
