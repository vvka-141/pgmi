// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../PROTOCOL.md ("MCP").
//
// POST /mcp: Streamable HTTP with JSON answers only (no SSE, no session),
// both protocol eras. There is no MCP SDK here: the database is the MCP
// server. The gateway forwards the body and the MCP-Protocol-Version,
// Mcp-Method and Mcp-Name headers, as sent, to api.mcp_request_policy, whose
// refusal is the whole answer (400, nothing dispatched, nothing logged), then
// dispatches to api.mcp_handle_request. The one piece of protocol knowledge
// kept here is the status of a dispatched answer (mcpStatus).
//
// The caller is the agent of a connection a person approved: an OAuth access
// token this gateway verified (resource.go), or the connection's handle, sent
// as Authorization: Bearer cn_... or X-Connection. No other header names
// anyone.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// rpcError is a JSON-RPC error answer with its HTTP status.
type rpcError struct {
	status  int
	code    int
	message string
	data    map[string]any
	headers map[string]string
}

// mcpAnswer writes a JSON-RPC envelope. The MCP-Protocol-Version the client
// sent is echoed on every answer.
func mcpAnswer(w http.ResponseWriter, status int, envelope []byte, headers map[string]string) {
	for name, value := range headers {
		w.Header().Set(name, value)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(envelope)))
	w.WriteHeader(status)
	_, _ = w.Write(envelope) // the client hung up: nothing left to tell it
}

func (e rpcError) write(w http.ResponseWriter, id any) {
	body := map[string]any{"code": e.code, "message": e.message}
	if e.data != nil {
		body["data"] = e.data
	}
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": body})
	mcpAnswer(w, e.status, envelope, e.headers)
}

// mcpError maps an error of the MCP or JSON-RPC path to its JSON-RPC answer:
// the twin of writeError.
func (g *Gateway) mcpError(r *http.Request, err error) rpcError {
	var exhausted *RetriesExhaustedError
	var failed *InterceptorFailedError
	switch {
	case errors.As(err, &exhausted):
		// The transaction lost a race every time it ran: nothing was
		// applied, and the same call may succeed when repeated.
		return rpcError{status: http.StatusOK, code: -31000,
			message: "The call conflicted with a concurrent change and was rolled back; repeat it unchanged",
			data:    map[string]any{"code": "pgmi.transaction_retryable", "sqlstate": exhausted.SQLState, "attempts": exhausted.Attempts},
			headers: map[string]string{"Retry-After": retryAfter}}
	case errors.Is(err, ErrUnavailable):
		g.Log.WarnContext(r.Context(), "no pooled connection", "path", r.URL.Path, "error", err)
		return rpcError{status: http.StatusServiceUnavailable, code: -31000,
			message: "The database is unavailable; repeat the call later",
			data:    map[string]any{"code": "pgmi.unavailable"}, headers: map[string]string{"Retry-After": retryAfter}}
	case errors.As(err, &failed):
		return rpcError{status: http.StatusBadGateway, code: -32603, message: "The gateway could not complete the call",
			data: map[string]any{"code": "pgmi.interceptor_failed", "requestId": requestID(r)}}
	case deadlineExceeded(r, err):
		g.Log.WarnContext(r.Context(), "deadline exceeded", "request_id", requestID(r), "error", err)
		return rpcError{status: http.StatusGatewayTimeout, code: -32603, message: "The call ran past its deadline and was cancelled",
			data: map[string]any{"code": "pgmi.deadline_exceeded", "requestId": requestID(r)}}
	default:
		g.Log.ErrorContext(r.Context(), "dispatch failed", "path", r.URL.Path, "request_id", requestID(r), "error", err)
		return rpcError{status: http.StatusInternalServerError, code: -32603, message: "Internal error"}
	}
}

// mcpStatus is the HTTP status of a dispatched answer (../PROTOCOL.md, "MCP
// status codes"). In the 2026-07-28 era (modern, from the policy) -32601 is
// 404 and -32022 is 400; every other answer is 200, a tool's -32602 included:
// the client reads JSON-RPC errors from the body. Every legacy answer is 200.
func mcpStatus(envelope []byte, modern bool) int {
	if !modern {
		return http.StatusOK
	}
	var answer struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(envelope, &answer) != nil || answer.Error == nil {
		return http.StatusOK
	}
	switch answer.Error.Code {
	case -32601:
		return http.StatusNotFound
	case -32022:
		return http.StatusBadRequest
	}
	return http.StatusOK
}

// handleUnknown: the database knows no live connection by the handle sent.
// The gateway answers 401, as for a token it refused.
func handleUnknown(envelope []byte) bool {
	var answer struct {
		Error *struct {
			Code int `json:"code"`
			Data struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
	}
	return json.Unmarshal(envelope, &answer) == nil && answer.Error != nil &&
		answer.Error.Code == -31001 && answer.Error.Data.Code == "pgmi.connection_unknown"
}

// bearerHandle is a connection handle sent as Authorization: Bearer cn_...,
// or "". A cn_ handle is not an OAuth token and never reaches the verifier.
func bearerHandle(authorization string) string {
	scheme, credentials, _ := strings.Cut(authorization, " ")
	credentials = strings.TrimSpace(credentials)
	if strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(credentials, "cn_") {
		return credentials
	}
	return ""
}

// unauthorized writes the 401 for a credential that proves no one. With
// MCP_RESOURCE set its challenge names the protected-resource metadata
// (RFC 9728 §5.1).
func (g *Gateway) unauthorized(w http.ResponseWriter, code string) {
	challenge := `Bearer error="` + code + `"`
	if g.Resource != nil {
		challenge = g.Resource.Challenge(code)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	w.WriteHeader(http.StatusUnauthorized)
}

// acceptsJSON: an Accept header that admits application/json. The most
// specific matching range decides (application/json, then application/*, then
// */*), and q=0 there is an explicit refusal (RFC 9110 §12.5.1).
func acceptsJSON(accept string) bool {
	specificity := map[string]int{"application/json": 3, "application/*": 2, "*/*": 1}
	best, bestQ := 0, 0.0
	for _, entry := range strings.Split(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(entry))
		rank := specificity[mediaType]
		if err != nil || rank == 0 {
			continue
		}
		q, err := strconv.ParseFloat(params["q"], 64)
		if params["q"] == "" || err != nil {
			q = 1
		}
		if rank > best || (rank == best && q > bestQ) {
			best, bestQ = rank, q
		}
	}
	return bestQ > 0
}

// rpcID is the JSON-RPC id of a request object (a string or an integer), or nil.
func rpcID(message any) any {
	m, _ := message.(map[string]any)
	switch id := m["id"].(type) {
	case string:
		return id
	case json.Number:
		if _, err := id.Int64(); err == nil {
			return id
		}
	}
	return nil
}

// mcpRoute is what api.mcp_request_policy routes on: an interceptor that
// changes it sends the request to another handler, so the policy is resolved
// again.
func mcpRoute(message any) string {
	m, _ := message.(map[string]any)
	params, _ := m["params"].(map[string]any)
	route, _ := json.Marshal([]any{m["method"], params["name"], params["uri"]})
	return string(route)
}

// restated is the Mcp-Method and Mcp-Name a rewritten request states, for the
// second policy call: the client's own were checked against the body it sent.
func restated(message any, version *string) MCPHeaders {
	m, _ := message.(map[string]any)
	params, _ := m["params"].(map[string]any)
	method, _ := m["method"].(string)
	key := "name"
	if method == "resources/read" {
		key = "uri"
	}
	h := MCPHeaders{ProtocolVersion: version, Method: &method}
	if name, ok := params[key].(string); ok {
		h.Name = &name
	}
	return h
}

func optionalHeader(r *http.Request, name string) *string {
	if values := r.Header.Values(name); len(values) > 0 {
		return &values[0]
	}
	return nil
}

// mcpCaller is the MCP context for the database, or false once the answer is
// written. A connection handle comes as Authorization: Bearer cn_... or, with
// no other Authorization, as X-Connection, and goes on as it is: the
// database resolves it. With MCP_RESOURCE set, any other bearer token is
// verified here and only the identity it proves goes on; a request with
// neither is 401, so a client learns where to get a token.
func (g *Gateway) mcpCaller(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	authorization := r.Header.Get("Authorization")
	handle := bearerHandle(authorization)
	if handle == "" && (g.Resource == nil || authorization == "") {
		handle = r.Header.Get("X-Connection")
	}
	if handle != "" || g.Resource == nil {
		if handle == "" {
			return nil, true
		}
		raw, _ := json.Marshal(map[string]string{"connection": handle})
		return raw, true
	}
	identity, err := g.Resource.Verify(r.Context(), authorization)
	var rejected *TokenRejectedError
	switch {
	case errors.As(err, &rejected):
		// The reason is logged, the token never is.
		g.Log.InfoContext(r.Context(), "token refused", "reason", rejected.Reason)
		g.unauthorized(w, rejected.Code)
		return nil, false
	case err != nil:
		g.Log.WarnContext(r.Context(), "identity provider unavailable", "error", err)
		rpcError{status: http.StatusServiceUnavailable, code: -31000,
			message: "The identity provider is unavailable; repeat the call later",
			data:    map[string]any{"code": "pgmi.identity_provider_unavailable"},
			headers: map[string]string{"Retry-After": "5"}}.write(w, nil)
		return nil, false
	}
	if ok, wait := g.PerClient.Allow(identity.Issuer + "\x00" + identity.Subject + "\x00" + identity.ClientID); !ok {
		g.writeError(w, r, rateLimited(wait))
		return nil, false
	}
	return identity.Context(), true
}

// mcp serves POST /mcp. The per-address rate limit and the Origin check ran
// as middleware before it.
func (g *Gateway) mcp(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		g.writeError(w, r, &problem{status: http.StatusUnsupportedMediaType, title: "Unsupported Media Type",
			code: "pgmi.unsupported_media_type", detail: "MCP requires Content-Type: application/json"})
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" && !acceptsJSON(accept) {
		g.writeError(w, r, &problem{status: http.StatusNotAcceptable, title: "Not Acceptable",
			code: "pgmi.not_acceptable", detail: "this endpoint answers application/json"})
		return
	}
	if version := r.Header.Get("MCP-Protocol-Version"); version != "" {
		w.Header().Set("MCP-Protocol-Version", version)
	}
	caller, ok := g.mcpCaller(w, r)
	if !ok {
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		g.writeError(w, r, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var message any
	if !utf8.Valid(body) || decoder.Decode(&message) != nil || decoder.More() {
		rpcError{status: http.StatusBadRequest, code: -32700, message: "Parse error"}.write(w, nil)
		return
	}
	id := rpcID(message)

	var requested string
	if value := r.Header.Get("X-PGMI-Transaction-Isolation"); value != "" {
		level, known := NormalizeIsolation(value)
		if !known {
			rpcError{status: http.StatusBadRequest, code: -32600, message: "Unsupported X-PGMI-Transaction-Isolation: " + value,
				data: map[string]any{"code": "pgmi.transaction_isolation_unsupported"}}.write(w, id)
			return
		}
		requested = level
	}

	object, _ := message.(map[string]any)
	method, _ := object["method"].(string)
	rc := newRequestContext(r, "mcp", method, "/mcp")
	ctx := r.Context()
	headers := MCPHeaders{optionalHeader(r, "MCP-Protocol-Version"), optionalHeader(r, "Mcp-Method"), optionalHeader(r, "Mcp-Name")}

	// One call before the transaction: the protocol checks (the database
	// owns them), the transaction policy and the handler's metadata.
	pol, err := g.Channel.MCPPolicy(ctx, rc, body, requested, headers)
	if err != nil {
		g.mcpError(r, err).write(w, id)
		return
	}
	if pol.Refusal == nil {
		rc = rc.withPolicy(pol)
		if selected := g.Pipeline.RequestsFor(rc); len(selected) > 0 {
			// The body is already parsed: the interceptors share it. On /mcp
			// only the body reaches the database.
			req := &Request{Method: http.MethodPost, URL: "/mcp",
				Message: Message{Header: IncomingHeaders(r.Header), Body: body, JSON: message, parsed: true}}
			route := mcpRoute(message)
			if err := g.Pipeline.RunRequests(ctx, rc, selected, req); err != nil {
				g.mcpError(r, err).write(w, id)
				return
			}
			body = req.Body
			if mcpRoute(req.JSON) != route {
				if pol, err = g.Channel.MCPPolicy(ctx, rc, body, requested, restated(req.JSON, headers.ProtocolVersion)); err != nil {
					g.mcpError(r, err).write(w, id)
					return
				}
				rc = rc.withPolicy(pol)
			}
		}
	}
	if pol.Refusal != nil {
		mcpAnswer(w, http.StatusBadRequest, pol.Refusal, nil)
		return
	}

	envelope, err := g.Channel.MCPRequest(ctx, rc, pol, body, caller)
	if err != nil {
		g.mcpError(r, err).write(w, id)
		return
	}
	if envelope == nil {
		// A notification: accepted, no answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if handleUnknown(envelope) {
		g.unauthorized(w, "invalid_token")
		return
	}
	status, answerHeaders := mcpStatus(envelope, pol.Modern), map[string]string{}
	if selected := g.Pipeline.ResponsesFor(rc); len(selected) > 0 {
		resp := &Response{Status: status, Message: Message{Header: answerHeaders, Body: envelope}}
		if err := g.Pipeline.RunResponses(ctx, rc, selected, resp); err != nil {
			g.mcpError(r, err).write(w, id)
			return
		}
		status, answerHeaders, envelope = resp.Status, resp.Header, resp.Body
	}
	mcpAnswer(w, status, envelope, answerHeaders)
}
