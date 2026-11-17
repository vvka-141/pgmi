// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project.
//
// The interceptor pipeline and the two examples: no database needed.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a request interceptor that adds a field, fails, or blocks.
type recorder struct {
	name      string
	needsJSON bool
	failure   Failure
	timeout   time.Duration
	add       string
	err       error
	block     time.Duration
}

func (r recorder) Spec() Spec {
	return Spec{Name: r.name, NeedsJSON: r.needsJSON, Failure: r.failure, Timeout: cmp.Or(r.timeout, time.Second)}
}

func (recorder) Applies(RequestContext) bool { return true }

func (r recorder) OnRequest(_ context.Context, _ RequestContext, req *Request) error {
	if r.block > 0 {
		time.Sleep(r.block) // deliberately deaf to ctx: the pipeline must still give up
	}
	if args, ok := req.JSON.(map[string]any); ok && r.add != "" {
		args[r.add] = true
	}
	req.Header["x-"+r.name] = "seen"
	return r.err
}

func request(body string) *Request {
	return &Request{Method: "POST", URL: "/x", Message: Message{Header: map[string]string{}, Body: []byte(body)}}
}

func jsonOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return v
}

func pipeline(requests ...RequestInterceptor) *Pipeline {
	return &Pipeline{Requests: requests, Log: quiet}
}

func TestNoInterceptorAppliesAndNothingIsSelected(t *testing.T) {
	var nothing *Pipeline
	if len(nothing.RequestsFor(rc)) != 0 || len(nothing.ResponsesFor(rc)) != 0 {
		t.Error("a nil pipeline selects nothing")
	}
	p := &Pipeline{Requests: []RequestInterceptor{EmbedFields{Embedder: FakeEmbedder{}}}, Log: quiet}
	if len(p.RequestsFor(rc)) != 0 {
		t.Error("embed applies only where an annotation asks for it")
	}
}

func TestAnInterceptorThatNeedsNoJSONLeavesTheBytesAlone(t *testing.T) {
	body := `{ "a" :1,  "n": 1.50 }`
	req := request(body)
	if err := pipeline(recorder{name: "h"}).RunRequests(t.Context(), rc, []RequestInterceptor{recorder{name: "h"}}, req); err != nil {
		t.Fatal(err)
	}
	if string(req.Body) != body || req.parsed || req.Header["x-h"] != "seen" {
		t.Errorf("body %q parsed %v headers %v", req.Body, req.parsed, req.Header)
	}
}

func TestJSONIsParsedOnceAndSerializedOnce(t *testing.T) {
	selected := []RequestInterceptor{recorder{name: "one", needsJSON: true, add: "one"}, recorder{name: "two", needsJSON: true, add: "two"}}
	req := request(`{"a":1,"n":1.50,"t":"<é>"}`)
	if err := pipeline(selected...).RunRequests(t.Context(), rc, selected, req); err != nil {
		t.Fatal(err)
	}
	want := `{"a":1,"n":1.50,"one":true,"t":"<é>","two":true}`
	if string(req.Body) != want {
		t.Errorf("body %s, want %s (numbers as sent, no HTML escaping)", req.Body, want)
	}
}

func TestABodyThatIsNotJSONPassesAsItWas(t *testing.T) {
	selected := []RequestInterceptor{recorder{name: "j", needsJSON: true, add: "x"}}
	for _, body := range []string{"not json", "", "{} {}"} {
		req := request(body)
		if err := pipeline(selected...).RunRequests(t.Context(), rc, selected, req); err != nil || string(req.Body) != body {
			t.Errorf("%q became %q (%v)", body, req.Body, err)
		}
	}
}

func TestFailStopsTheRequest(t *testing.T) {
	selected := []RequestInterceptor{recorder{name: "bad", err: errors.New("down")}, recorder{name: "after"}}
	req := request(`{}`)
	err := pipeline(selected...).RunRequests(t.Context(), rc, selected, req)
	var failed *InterceptorFailedError
	if !errors.As(err, &failed) || failed.Name != "bad" || failed.Applied {
		t.Fatalf("got %v", err)
	}
	if _, ran := req.Header["x-after"]; ran {
		t.Error("an interceptor after a failure ran")
	}
}

func TestDegradeGoesOnWithTheRequestAsItWas(t *testing.T) {
	selected := []RequestInterceptor{
		recorder{name: "broken", needsJSON: true, add: "broken", failure: Degrade, err: errors.New("down")},
		recorder{name: "after", needsJSON: true, add: "after"},
	}
	req := request(`{"a":1}`)
	if err := pipeline(selected...).RunRequests(t.Context(), rc, selected, req); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, req.Body); got["broken"] != nil || got["after"] != true || req.Header["x-broken"] != "" {
		t.Errorf("body %s headers %v", req.Body, req.Header)
	}
}

func TestATimeoutIsAFailureAndALateChangeIsNeverRead(t *testing.T) {
	for _, failure := range []Failure{Fail, Degrade} {
		slow := recorder{name: "slow", needsJSON: true, add: "late", failure: failure, timeout: 20 * time.Millisecond, block: 200 * time.Millisecond}
		req := request(`{"a":1}`)
		started := time.Now()
		err := pipeline(slow).RunRequests(t.Context(), rc, []RequestInterceptor{slow}, req)
		if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
			t.Errorf("waited %v for an interceptor with a 20 ms timeout", elapsed)
		}
		switch failure {
		case Fail:
			if err == nil {
				t.Error("a timeout must fail the request")
			}
		case Degrade:
			if err != nil || strings.Contains(string(req.Body), "late") {
				t.Errorf("degrade: %v, body %s", err, req.Body)
			}
		}
	}
	time.Sleep(250 * time.Millisecond) // let the abandoned goroutines finish before -race checks the next test
}

func TestTheRequestDeadlineBoundsEveryInterceptor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	blocked := EmbedFields{Embedder: blockingEmbedder{}, Timeout: time.Minute}
	r := rc
	r.Annotations = map[string]map[string]any{"text": {"x-embed": true}}
	started := time.Now()
	err := pipeline(blocked).RunRequests(ctx, r, []RequestInterceptor{blocked}, request(`{"text":"a"}`))
	if err == nil || time.Since(started) > time.Second {
		t.Errorf("%v after %v", err, time.Since(started))
	}
}

type blockingEmbedder struct{}

func (blockingEmbedder) Embed(ctx context.Context, _ string) ([]float64, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestConcurrentRequestsNeverSeeEachOther(t *testing.T) {
	embed := EmbedFields{Embedder: FakeEmbedder{}, Timeout: time.Second}
	p := pipeline(embed)
	r := rc
	r.Annotations = map[string]map[string]any{"text": {"x-embed": true}}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			text := fmt.Sprintf("text %d", i)
			req := request(fmt.Sprintf(`{"text":%q}`, text))
			if err := p.RunRequests(t.Context(), r, p.RequestsFor(r), req); err != nil {
				t.Error(err)
				return
			}
			want, _ := FakeEmbedder{}.Embed(t.Context(), text)
			got := jsonOf(t, req.Body)
			if fmt.Sprint(got["text_embedding"]) != fmt.Sprint(want) || got["text"] != text {
				t.Errorf("%s: %v", text, got)
			}
		})
	}
	wg.Wait()
}

func TestTheFakeEmbedderMatchesPROTOCOL(t *testing.T) {
	// The vectors every language's fake must produce (../PROTOCOL.md).
	for text, want := range map[string]string{"hello": "[0.1725 0.949 0.302 0.7294]", "héllo wörld": "[0.6314 0 0.2471 0.4902]"} {
		got, err := FakeEmbedder{}.Embed(t.Context(), text)
		if err != nil || fmt.Sprint(got) != want {
			t.Errorf("%q: %v %v, want %s", text, got, err, want)
		}
	}
	if _, err := (FakeEmbedder{}).Embed(t.Context(), strings.Repeat("é", 2000)); err != nil {
		t.Errorf("2000 characters are allowed: %v", err)
	}
	if _, err := (FakeEmbedder{}).Embed(t.Context(), strings.Repeat("x", 2001)); err == nil {
		t.Error("over 2000 characters must be refused")
	}
}

func TestEmbedFields(t *testing.T) {
	embed := EmbedFields{Embedder: FakeEmbedder{}, Timeout: time.Second}
	annotated := rc
	annotated.Annotations = map[string]map[string]any{"text": {"x-embed": true}, "other": {"x-embed": false}}
	if !embed.Applies(annotated) || embed.Applies(rc) {
		t.Error("selected by the x-embed annotation only")
	}
	run := func(r RequestContext, body string) map[string]any {
		req := request(body)
		if err := pipeline(embed).RunRequests(t.Context(), r, []RequestInterceptor{embed}, req); err != nil {
			t.Fatal(err)
		}
		return jsonOf(t, req.Body)
	}

	got := run(annotated, `{"text":"hello","other":"x","blank":" "}`)
	if fmt.Sprint(got["text_embedding"]) != "[0.1725 0.949 0.302 0.7294]" || got["other_embedding"] != nil {
		t.Errorf("rest body: %v", got)
	}
	if got := run(annotated, `{"text":"   "}`); got["text_embedding"] != nil {
		t.Errorf("a blank text is not embedded: %v", got)
	}
	if got := run(annotated, `{"text":"a","text_embedding":[1]}`); fmt.Sprint(got["text_embedding"]) != "[1]" {
		t.Errorf("a vector the client sent is kept: %v", got)
	}

	mcp := annotated
	mcp.Protocol = "mcp"
	got = run(mcp, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t","arguments":{"text":"hello"}}}`)
	args := got["params"].(map[string]any)["arguments"].(map[string]any)
	if fmt.Sprint(args["text_embedding"]) != "[0.1725 0.949 0.302 0.7294]" || got["text_embedding"] != nil {
		t.Errorf("mcp arguments: %v", got)
	}
}

func TestRequestIDHeaderNeverParsesTheBody(t *testing.T) {
	resp := &Response{Status: 200, Message: Message{Header: map[string]string{}, Body: []byte("not json")}}
	p := &Pipeline{Responses: []ResponseInterceptor{RequestIDHeader{}}, Log: quiet}
	if err := p.RunResponses(t.Context(), rc, p.ResponsesFor(rc), resp); err != nil {
		t.Fatal(err)
	}
	if resp.Header["x-request-id"] != "r1" || string(resp.Body) != "not json" || resp.parsed {
		t.Errorf("%+v", resp)
	}
}
