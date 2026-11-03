// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../PROTOCOL.md ("Extension seams").
//
// Request and response interceptors: an ordered pipeline around each dispatch.
//
// A request interceptor runs after the policy call and before the
// transaction opens; it may change the method, URL, headers and body. A
// response interceptor runs after the commit; it may change the status,
// headers and body. Uses: embedding vectors for text fields, enrichment from
// another service, signing storage URLs the handler returned, redaction.
//
// The rules:
//
//   - The database still decides. An interceptor never decides access or
//     validity; it selects itself from what the policy call returned (the
//     handler, the x-* annotations on its inputSchema), never from a path list.
//   - Zero cost when unused. No interceptor applies: the body passes as bytes,
//     never parsed. Some need JSON: it is parsed once and serialized once for all.
//   - Failure policy per interceptor. Fail (the default) answers 502 with the
//     code pgmi.interceptor_failed; Degrade logs and goes on with the request
//     as it was before that interceptor. A timeout is a failure.
//   - No database connection is held while an interceptor runs: the policy
//     call has returned its connection, the dispatch has not taken one yet.
//   - Goroutine safety. The pipeline is built in main and only read
//     afterwards; interceptors keep no per-request state (it travels in the
//     message and the RequestContext). An outside service is called through
//     one long-lived client created in main and handed to the constructor.
//
// This file also holds the two examples main can enable (INTERCEPTORS):
// EmbedFields with a FakeEmbedder, and RequestIDHeader.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// Failure is what a failing interceptor does to the request.
type Failure int

const (
	Fail    Failure = iota // answer 502 pgmi.interceptor_failed
	Degrade                // log, and go on without this interceptor's changes
)

// Spec is what an interceptor declares about itself.
type Spec struct {
	Name      string        // shows in the log
	NeedsJSON bool          // reads Message.JSON; false leaves the body as bytes
	Failure   Failure       // Fail unless the request is still right without it
	Timeout   time.Duration // bounded by the request's deadline as well
}

// Message is headers and a body. When an interceptor that NeedsJSON runs, the
// pipeline parses Body into JSON once, and serializes JSON back into Body once
// after the last interceptor. A body that is not JSON leaves JSON nil and
// passes on as the bytes it was: the database answers it.
type Message struct {
	Header map[string]string // lower-case names
	Body   []byte
	JSON   any // map[string]any, []any, string, json.Number, bool or nil
	parsed bool
}

type Request struct {
	Method, URL string
	Message
}

type Response struct {
	Status int
	Message
}

// RequestInterceptor changes a request before its transaction opens.
type RequestInterceptor interface {
	Spec() Spec
	Applies(rc RequestContext) bool
	OnRequest(ctx context.Context, rc RequestContext, req *Request) error
}

// ResponseInterceptor changes an answer after its commit.
type ResponseInterceptor interface {
	Spec() Spec
	Applies(rc RequestContext) bool
	OnResponse(ctx context.Context, rc RequestContext, resp *Response) error
}

// InterceptorFailedError ends the request with 502. Applied says whether the
// transaction had already committed, so the client is not told to repeat a
// write that happened.
type InterceptorFailedError struct {
	Name    string
	Applied bool
	Err     error
}

func (e *InterceptorFailedError) Error() string {
	return "interceptor " + e.Name + ": " + e.Err.Error()
}
func (e *InterceptorFailedError) Unwrap() error { return e.Err }

// Pipeline holds the interceptors in order. A nil *Pipeline runs nothing.
type Pipeline struct {
	Requests  []RequestInterceptor
	Responses []ResponseInterceptor
	Log       *slog.Logger
}

func (p *Pipeline) RequestsFor(rc RequestContext) []RequestInterceptor {
	if p == nil {
		return nil
	}
	var selected []RequestInterceptor
	for _, i := range p.Requests {
		if i.Applies(rc) {
			selected = append(selected, i)
		}
	}
	return selected
}

func (p *Pipeline) ResponsesFor(rc RequestContext) []ResponseInterceptor {
	if p == nil {
		return nil
	}
	var selected []ResponseInterceptor
	for _, i := range p.Responses {
		if i.Applies(rc) {
			selected = append(selected, i)
		}
	}
	return selected
}

// RunRequests runs the selected request interceptors on req, in order.
func (p *Pipeline) RunRequests(ctx context.Context, rc RequestContext, selected []RequestInterceptor, req *Request) error {
	for _, i := range selected {
		work := req
		if i.Spec().Failure == Degrade {
			// Degrade must go on "as before": it works on a copy, adopted
			// only when it succeeds. Fail needs no copy: the request ends.
			work = &Request{Method: req.Method, URL: req.URL, Message: req.clone()}
		}
		kept, err := p.run(ctx, rc, i.Spec(), &work.Message, func(ctx context.Context) error { return i.OnRequest(ctx, rc, work) })
		if err != nil {
			return &InterceptorFailedError{Name: i.Spec().Name, Err: err}
		}
		if kept {
			*req = *work
		}
	}
	return req.finish()
}

// RunResponses runs the selected response interceptors on resp, in order.
func (p *Pipeline) RunResponses(ctx context.Context, rc RequestContext, selected []ResponseInterceptor, resp *Response) error {
	for _, i := range selected {
		work := resp
		if i.Spec().Failure == Degrade {
			work = &Response{Status: resp.Status, Message: resp.clone()}
		}
		kept, err := p.run(ctx, rc, i.Spec(), &work.Message, func(ctx context.Context) error { return i.OnResponse(ctx, rc, work) })
		if err != nil {
			return &InterceptorFailedError{Name: i.Spec().Name, Applied: true, Err: err}
		}
		if kept {
			*resp = *work
		}
	}
	return resp.finish()
}

// run gives one interceptor its timeout. It runs on a goroutine of its own so
// the timeout holds even when it blocks without watching ctx; one that
// overruns is abandoned, and its changes are never read (for Fail the
// request ends, for Degrade it worked on a copy that is dropped). kept is
// false when a Degrade interceptor failed: its changes are dropped.
func (p *Pipeline) run(ctx context.Context, rc RequestContext, spec Spec, m *Message, call func(context.Context) error) (kept bool, err error) {
	if spec.NeedsJSON {
		m.parse()
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	done := make(chan error, 1) // buffered: an abandoned goroutine never blocks
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- call(ctx)
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = fmt.Errorf("timed out: %w", ctx.Err())
	}
	switch {
	case err == nil:
		return true, nil
	case spec.Failure == Degrade:
		p.Log.WarnContext(ctx, "interceptor degraded", "request_id", rc.RequestID, "interceptor", spec.Name, "error", err)
		return false, nil
	default:
		p.Log.ErrorContext(ctx, "interceptor failed", "request_id", rc.RequestID, "interceptor", spec.Name, "error", err)
		return false, err
	}
}

// parse reads Body as JSON once; numbers stay json.Number so they serialize
// back exactly as sent.
func (m *Message) parse() {
	if m.parsed {
		return
	}
	m.parsed = true
	if len(m.Body) == 0 {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(m.Body))
	decoder.UseNumber()
	var v any
	if decoder.Decode(&v) == nil && !decoder.More() {
		m.JSON = v
	}
}

// finish serializes JSON into Body once, after the last interceptor.
func (m *Message) finish() error {
	if !m.parsed || m.JSON == nil {
		return nil
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(m.JSON); err != nil {
		return fmt.Errorf("serialize body: %w", err)
	}
	m.Body = bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	return nil
}

func (m *Message) clone() Message {
	header := make(map[string]string, len(m.Header))
	for k, v := range m.Header {
		header[k] = v
	}
	return Message{Header: header, Body: bytes.Clone(m.Body), JSON: deepCopy(m.JSON), parsed: m.parsed}
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = deepCopy(e)
		}
		return out
	default:
		return v
	}
}

// Arguments is the object the handler's inputSchema describes: the REST body,
// or an MCP call's params.arguments. nil when there is none.
func Arguments(m *Message, rc RequestContext) map[string]any {
	value := m.JSON
	if rc.Protocol == "mcp" {
		message, _ := value.(map[string]any)
		params, _ := message["params"].(map[string]any)
		value = params["arguments"]
	}
	args, _ := value.(map[string]any)
	return args
}

// -- Example: a computed field for every property the schema marks x-embed ---

// Embedder is the outside call behind an interface: a real one posts to an
// embedding API through the one shared http.Client; tests use the fake.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// FakeEmbedder is deterministic and offline (../PROTOCOL.md, "The examples"):
// the first four bytes of the SHA-256 of the UTF-8 text, each divided by 255
// and rounded to four places. Like a real model with a token limit, it
// refuses a text over 2000 characters.
type FakeEmbedder struct{}

const fakeEmbedderMaxChars = 2000

func (FakeEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	if len([]rune(text)) > fakeEmbedderMaxChars {
		return nil, fmt.Errorf("text over %d characters", fakeEmbedderMaxChars)
	}
	digest := sha256.Sum256([]byte(text))
	vector := make([]float64, 4)
	for i, b := range digest[:4] {
		vector[i] = math.Round(float64(b)/255*10000) / 10000
	}
	return vector, nil
}

// EmbedFields adds, for each inputSchema property marked "x-embed": true, a
// <property>_embedding sibling holding its vector. The schema declares the
// sibling too, so the database validates it like any other field. A sibling
// already present is left alone: a client that embeds for itself is not
// embedded twice. The fields are embedded concurrently.
//
// ADAPT: an embedding outage can Degrade (the handler searches without the
// vector) or Fail; choose per project.
type EmbedFields struct {
	Embedder Embedder
	Failure  Failure
	Timeout  time.Duration
}

func (e EmbedFields) Spec() Spec {
	return Spec{Name: "embed", NeedsJSON: true, Failure: e.Failure, Timeout: e.Timeout}
}

func (e EmbedFields) Applies(rc RequestContext) bool {
	for _, a := range rc.Annotations {
		if a["x-embed"] == true {
			return true
		}
	}
	return false
}

func (e EmbedFields) OnRequest(ctx context.Context, rc RequestContext, req *Request) error {
	args := Arguments(&req.Message, rc)
	if args == nil {
		return nil
	}
	var fields []string
	for name, a := range rc.Annotations {
		text, _ := args[name].(string)
		if _, sent := args[name+"_embedding"]; a["x-embed"] == true && strings.TrimSpace(text) != "" && !sent {
			fields = append(fields, name)
		}
	}
	vectors := make([][]float64, len(fields)) // each goroutine writes its own index
	g, ctx := errgroup.WithContext(ctx)
	for i, name := range fields {
		g.Go(func() error {
			vector, err := e.Embedder.Embed(ctx, args[name].(string))
			if err != nil {
				return fmt.Errorf("embed %s: %w", name, err)
			}
			vectors[i] = vector
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	for i, name := range fields {
		args[name+"_embedding"] = vectors[i]
	}
	return nil
}

// -- Example: a response header ---------------------------------------------

// RequestIDHeader answers the gateway's request id in X-Request-Id, the id its
// log lines carry. Headers only: the body is never parsed.
type RequestIDHeader struct{}

func (RequestIDHeader) Spec() Spec {
	return Spec{Name: "request-id", Timeout: time.Second}
}

func (RequestIDHeader) Applies(RequestContext) bool { return true }

func (RequestIDHeader) OnResponse(_ context.Context, rc RequestContext, resp *Response) error {
	resp.Header["x-request-id"] = rc.RequestID
	return nil
}
