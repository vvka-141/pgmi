// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../PROTOCOL.md.
//
// POST /rpc: pgmi's own JSON-RPC 2.0 endpoint, for named actions such as
// organization.transfer_ownership. One request per POST; its method names the
// handler. The database checks the envelope, the sessions and the access, and
// answers status, headers and body, which this route copies. Identity and
// CSRF are those of /api: the login session comes from the cookie.
package main

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
)

const rpcPath = "/rpc"

func (g *Gateway) rpc(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		g.writeError(w, r, &problem{status: http.StatusUnsupportedMediaType, title: "Unsupported Media Type",
			code: "pgmi.unsupported_media_type", detail: "JSON-RPC requires Content-Type: application/json"})
		return
	}
	if name := HeaderCollision(r.Header); name != "" {
		g.writeError(w, r, &problem{status: http.StatusBadRequest, title: "Bad Request", code: "pgmi.header_collision",
			detail: "header " + name + " is sent more than once"})
		return
	}
	loginSession := cookieValue(r, sessionCookie)
	if !g.Sessions.CSRFOK(r.Method, loginSession, r.Header.Get("X-CSRF-Token")) {
		g.writeError(w, r, csrfRefused())
		return
	}
	headers := ForwardedHeaders(r.Header, loginSession)
	requested, err := requestedIsolation(headers)
	if err != nil {
		g.writeError(w, r, err)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		g.writeError(w, r, err)
		return
	}

	// The method picks the transaction policy; a body that names none, or is
	// no request at all, still goes to the database, which answers it.
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var message any
	_ = decoder.Decode(&message)
	object, _ := message.(map[string]any)
	method, _ := object["method"].(string)
	if strings.ContainsRune(method, 0) {
		// PostgreSQL text holds no NUL: no method, so the database answers
		// the envelope instead of the call failing.
		method = ""
	}
	id := rpcID(message)

	rc := newRequestContext(r, "rpc", method, rpcPath)
	ctx := r.Context()
	// pgmi-specific: the database decides, before the transaction opens.
	// ADAPT: run the interceptor pipeline here, as rest does, if RPC needs it.
	pol, err := g.Channel.RPCPolicy(ctx, rc, requested)
	if err != nil {
		g.mcpError(r, err).write(w, id)
		return
	}
	result, err := g.Channel.RPCInvoke(ctx, rc.withPolicy(pol), pol, headers, body)
	if err != nil {
		g.mcpError(r, err).write(w, id)
		return
	}
	writeResult(w, r, result)
}
