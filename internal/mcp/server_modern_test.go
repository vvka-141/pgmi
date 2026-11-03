package mcp

import (
	"slices"
	"testing"
)

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

func resultMap(t *testing.T, r rpcResponse) map[string]any {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("unexpected error %d: %s", r.Error.Code, r.Error.Message)
	}
	var m map[string]any
	mustRemarshal(t, r.Result, &m)
	return m
}

func TestServerDiscoverReturnsAComplete2026Result(t *testing.T) {
	s := NewServer("pgmi", "1.2.3")
	resp := runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+modernMeta+`}}`)
	m := resultMap(t, resp[0])

	for _, key := range []string{"resultType", "supportedVersions", "capabilities", "ttlMs", "cacheScope"} {
		if _, ok := m[key]; !ok {
			t.Errorf("DiscoverResult lacks required field %q: %v", key, m)
		}
	}
	if m["resultType"] != "complete" || m["cacheScope"] != "public" {
		t.Errorf("resultType=%v cacheScope=%v", m["resultType"], m["cacheScope"])
	}
	var versions []string
	mustRemarshal(t, m["supportedVersions"], &versions)
	if !slices.Equal(versions, ModernVersions) {
		t.Errorf("supportedVersions = %v, want only the versions a request can name in _meta: %v", versions, ModernVersions)
	}
	caps, _ := m["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities must declare tools: %v", m["capabilities"])
	}
	meta, _ := m["_meta"].(map[string]any)
	info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if info["name"] != "pgmi" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", meta)
	}
}

func TestServerDiscoverWithoutMetaStillAnswers(t *testing.T) {
	resp := runSession(t, NewServer("pgmi", "x"), `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`)
	if resultMap(t, resp[0])["resultType"] != "complete" {
		t.Errorf("a bare discover probe must still get a DiscoverResult: %+v", resp[0])
	}
}

func TestModernToolsCallWorksWithoutInitialize(t *testing.T) {
	s := NewServer("pgmi", "x")
	s.Register(echoTool())
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"a":1},`+modernMeta+`}}`)
	if len(resp) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resp))
	}
	for _, r := range resp {
		if resultMap(t, r)["resultType"] != "complete" {
			t.Errorf("a 2026-07-28 result must carry resultType complete: %+v", r)
		}
	}
	var call callToolResult
	mustRemarshal(t, resp[1].Result, &call)
	if call.IsError || len(call.Content) == 0 {
		t.Errorf("tools/call failed: %+v", call)
	}
}

func TestModernRequestRequiresClientCapabilities(t *testing.T) {
	s := NewServer("pgmi", "x")
	s.Register(echoTool())
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)
	if resp[0].Error == nil || resp[0].Error.Code != codeInvalidParams {
		t.Fatalf("a modern request without clientCapabilities must get -32602, got %+v", resp[0])
	}
}

func TestModernRequestWithAnUnknownVersionIsRefused(t *testing.T) {
	for _, method := range []string{"server/discover", "tools/list"} {
		t.Run(method, func(t *testing.T) {
			resp := runSession(t, NewServer("pgmi", "x"),
				`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`)
			e := resp[0].Error
			if e == nil || e.Code != codeUnsupportedProtocolVersion {
				t.Fatalf("want -32022, got %+v", resp[0])
			}
			var data struct {
				Supported []string `json:"supported"`
				Requested string   `json:"requested"`
			}
			mustRemarshal(t, e.Data, &data)
			if !slices.Equal(data.Supported, ModernVersions) || data.Requested != "2099-01-01" {
				t.Errorf("data = %+v", data)
			}
		})
	}
}

func TestLegacySessionIsUnchanged(t *testing.T) {
	s := NewServer("pgmi", "x")
	s.Register(echoTool())
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"a":1}}}`)
	var init initializeResult
	mustRemarshal(t, resp[0].Result, &init)
	if init.ProtocolVersion != "2025-11-25" {
		t.Errorf("initialize negotiated %q", init.ProtocolVersion)
	}
	if _, ok := resultMap(t, resp[1])["resultType"]; ok {
		t.Error("a legacy result must not gain resultType")
	}
}

// The era is chosen per request: one process serves both.
func TestBothErasInOneProcess(t *testing.T) {
	s := NewServer("pgmi", "x")
	s.Register(echoTool())
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+modernMeta+`}}`+"\n"+
			`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	_, modern := resultMap(t, resp[1])["resultType"]
	_, legacy := resultMap(t, resp[2])["resultType"]
	if !modern || legacy {
		t.Errorf("modern request resultType=%v, legacy request resultType=%v; want true, false", modern, legacy)
	}
	var ids []string
	for _, r := range resp {
		ids = append(ids, string(r.ID))
	}
	if !slices.Equal(ids, []string{"1", "2", "3"}) {
		t.Errorf("ids %v", ids)
	}
}

func TestModernInitializeAndPingAreMethodNotFound(t *testing.T) {
	s := NewServer("pgmi", "x")
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28",`+modernMeta+`}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"ping","params":{`+modernMeta+`}}`)
	for _, r := range resp {
		if r.Error == nil || r.Error.Code != codeMethodNotFound {
			t.Errorf("a 2026-07-28 %s must be -32601, got %+v", string(r.ID), r)
		}
	}
}

func TestEveryModernResultNamesTheServer(t *testing.T) {
	s := NewServer("pgmi", "1.2.3")
	s.Register(echoTool())
	resp := runSession(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{},`+modernMeta+`}}`)
	for _, r := range resp {
		meta, _ := resultMap(t, r)["_meta"].(map[string]any)
		info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
		if info["name"] != "pgmi" || info["version"] != "1.2.3" {
			t.Errorf("result %s lacks serverInfo in _meta: %+v", string(r.ID), r.Result)
		}
	}
}

func TestModernToolsListCarriesCachingHints(t *testing.T) {
	s := NewServer("pgmi", "x")
	s.Register(echoTool())
	m := resultMap(t, runSession(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`)[0])
	if _, ok := m["ttlMs"].(float64); !ok || m["cacheScope"] != "public" {
		t.Errorf("a 2026-07-28 tools/list must carry ttlMs and cacheScope: %v", m)
	}
}
