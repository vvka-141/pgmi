// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project.
//
// The channel decorators against a fake channel: no database needed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeChannel answers each call with the next error of its list (nil: success)
// and counts the calls.
type fakeChannel struct {
	errs  []error
	calls int
}

func (f *fakeChannel) next() error {
	f.calls++
	if len(f.errs) == 0 {
		return nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return err
}

func (f *fakeChannel) RestPolicy(context.Context, RequestContext, string) (Policy, error) {
	return Policy{Handler: "h"}, f.next()
}

func (f *fakeChannel) RestInvoke(context.Context, RequestContext, Policy, map[string]string, []byte) (RestResult, error) {
	return RestResult{Status: 200}, f.next()
}

func (f *fakeChannel) RPCPolicy(context.Context, RequestContext, string) (Policy, error) {
	return Policy{Handler: "h"}, f.next()
}

func (f *fakeChannel) RPCInvoke(context.Context, RequestContext, Policy, map[string]string, []byte) (RestResult, error) {
	return RestResult{Status: 200}, f.next()
}

func (f *fakeChannel) MCPPolicy(context.Context, RequestContext, []byte, string, MCPHeaders) (Policy, error) {
	return Policy{}, f.next()
}

func (f *fakeChannel) MCPRequest(context.Context, RequestContext, Policy, []byte, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"result":{}}`), f.next()
}

var (
	lost          = io.ErrUnexpectedEOF
	serialization = &pgconn.PgError{Code: "40001"}
	rc            = RequestContext{RequestID: "r1", Protocol: "rest", Method: "GET", URL: "/x"}
)

func TestALostConnectionRetriesThePolicyLookups(t *testing.T) {
	f := &fakeChannel{errs: []error{lost}}
	c := ConnectionRetry{Channel: f, Attempts: 2, Log: quiet}
	if _, err := c.RestPolicy(t.Context(), rc, ""); err != nil || f.calls != 2 {
		t.Errorf("rest policy: %v after %d calls", err, f.calls)
	}
	f = &fakeChannel{errs: []error{lost}}
	c.Channel = f
	if _, err := c.MCPPolicy(t.Context(), rc, nil, "", MCPHeaders{}); err != nil || f.calls != 2 {
		t.Errorf("mcp policy: %v after %d calls", err, f.calls)
	}
}

func TestAFailedConnectIsLoggedWithItsCause(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://gateway:s3cret-pw@127.0.0.1:1/app?connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var out bytes.Buffer
	g := &Gateway{Channel: &Postgres{Pool: pool, AcquireTimeout: 5 * time.Second}, Log: slog.New(slog.NewTextHandler(&out, nil))}
	resp := serve(g, httptest.NewRequest(http.MethodGet, "/api/hello", nil))
	var body struct{ Code, Detail string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || body.Code != "pgmi.unavailable" || body.Detail != "no database connection is free; repeat the request" {
		t.Errorf("answer changed: %d %+v", resp.StatusCode, body)
	}
	if log := out.String(); !strings.Contains(log, "failed to connect") || !strings.Contains(log, "127.0.0.1:1") || strings.Contains(log, "s3cret-pw") {
		t.Errorf("log must name the connect error and never the password:\n%s", log)
	}
}

func TestALostConnectionRetriesAReadOnlyDispatch(t *testing.T) {
	f := &fakeChannel{errs: []error{lost}}
	c := ConnectionRetry{Channel: f, Attempts: 2, Log: quiet}
	if _, err := c.RestInvoke(t.Context(), rc, Policy{ReadOnly: true}, nil, nil); err != nil || f.calls != 2 {
		t.Errorf("%v after %d calls", err, f.calls)
	}
}

func TestAWriteIsNeverSentTwice(t *testing.T) {
	f := &fakeChannel{errs: []error{lost}}
	c := ConnectionRetry{Channel: f, Attempts: 2, Log: quiet}
	if _, err := c.RestInvoke(t.Context(), rc, Policy{}, nil, nil); !errors.Is(err, lost) || f.calls != 1 {
		t.Errorf("rest write: %v after %d calls", err, f.calls)
	}
	f = &fakeChannel{errs: []error{lost}}
	c.Channel = f
	if _, err := c.MCPRequest(t.Context(), rc, Policy{}, nil, nil); !errors.Is(err, lost) || f.calls != 1 {
		t.Errorf("mcp: %v after %d calls", err, f.calls)
	}
}

func TestAnAnswerFromTheServerIsNotALostConnection(t *testing.T) {
	for _, err := range []error{serialization, &pgconn.PgError{Code: "42883"}, ErrUnavailable, context.DeadlineExceeded} {
		f := &fakeChannel{errs: []error{err}}
		c := ConnectionRetry{Channel: f, Attempts: 2, Log: quiet}
		if _, got := c.RestPolicy(t.Context(), rc, ""); !errors.Is(got, err) || f.calls != 1 {
			t.Errorf("%v: %v after %d calls", err, got, f.calls)
		}
	}
	for _, err := range []error{&pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "57P01"}, io.EOF} {
		if !connectionLost(err) {
			t.Errorf("%v is a lost connection", err)
		}
	}
}

func TestSerializationFailuresRerunTheWholeTransaction(t *testing.T) {
	f := &fakeChannel{errs: []error{serialization, serialization}}
	s := SerializationRetry{Channel: f, Attempts: 3}
	if _, err := s.RestInvoke(t.Context(), rc, Policy{}, nil, nil); err != nil || f.calls != 3 {
		t.Errorf("%v after %d calls", err, f.calls)
	}

	f = &fakeChannel{errs: []error{serialization, serialization, &pgconn.PgError{Code: "40P01"}}}
	s.Channel = f
	_, err := s.MCPRequest(t.Context(), rc, Policy{}, nil, nil)
	var exhausted *RetriesExhaustedError
	if !errors.As(err, &exhausted) || exhausted.Attempts != 3 || exhausted.SQLState != "40P01" || f.calls != 3 {
		t.Errorf("%v after %d calls", err, f.calls)
	}

	f = &fakeChannel{errs: []error{serialization}}
	s.Channel = f
	if _, err := s.RestPolicy(t.Context(), rc, ""); !errors.Is(err, serialization) || f.calls != 1 {
		t.Errorf("a policy lookup is not a transaction to retry: %v after %d calls", err, f.calls)
	}
}

func TestTracingLogsEachCallWithTheRequestIDAndNoBody(t *testing.T) {
	var out bytes.Buffer
	tr := Tracing{Channel: &fakeChannel{errs: []error{nil, serialization}}, Log: slog.New(slog.NewTextHandler(&out, nil))}
	_, _ = tr.RestInvoke(t.Context(), rc, Policy{}, map[string]string{"x-login-session": "ls_secret"}, []byte(`{"secret":1}`))
	_, _ = tr.RestInvoke(t.Context(), rc, Policy{}, nil, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "request_id=r1") || !strings.Contains(lines[0], "status=200") ||
		!strings.Contains(lines[1], "sqlstate=40001") {
		t.Errorf("trace:\n%s", out.String())
	}
	if strings.Contains(out.String(), "secret") {
		t.Errorf("a body or a header reached the log:\n%s", out.String())
	}
}

func TestTheDecoratorsCompose(t *testing.T) {
	f := &fakeChannel{errs: []error{serialization}}
	var out bytes.Buffer
	c := ConnectionRetry{Attempts: 2, Log: quiet, Channel: SerializationRetry{Attempts: 3,
		Channel: Tracing{Log: slog.New(slog.NewTextHandler(&out, nil)), Channel: f}}}
	if _, err := c.RestInvoke(t.Context(), rc, Policy{}, nil, nil); err != nil || f.calls != 2 {
		t.Errorf("%v after %d calls", err, f.calls)
	}
	if n := strings.Count(out.String(), "op=rest.invoke"); n != 2 {
		t.Errorf("every attempt is a trace line of its own; got %d:\n%s", n, out.String())
	}
}

func TestTheStatementTimeoutIsWhatTheRequestHasLeft(t *testing.T) {
	if got, err := statementTimeout(t.Context()); got != "" || err != nil {
		t.Errorf("no deadline: %q %v, want the server's setting", got, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	got, err := statementTimeout(ctx)
	if ms, _ := strconv.Atoi(got); err != nil || ms < 1490 || ms > 1500 {
		t.Errorf("1.5 s left: %q %v", got, err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 100*time.Microsecond)
	defer cancel()
	if got, err := statementTimeout(ctx); err == nil && got != "1" {
		t.Errorf("under a millisecond left rounds up, never to 0: %q", got)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if got, err := statementTimeout(ctx); !errors.Is(err, ErrDeadlineExceeded) || got != "" {
		t.Errorf("nothing left: %q %v", got, err)
	}
}

func TestACancelledStatementIsTheDeadlineOnlyOnceItPassed(t *testing.T) {
	cancelled := &pgconn.PgError{Code: "57014"}
	past, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	live, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, c := range []struct {
		name     string
		ctx      context.Context
		err      error
		deadline bool
	}{
		{"57014 past the deadline", past, cancelled, true},
		{"57014 before it", live, cancelled, false},
		{"the context's deadline", live, fmt.Errorf("query: %w", context.DeadlineExceeded), true},
		{"another error past it", past, serialization, false},
		{"no error", past, nil, false},
	} {
		got := asDeadline(c.ctx, c.err)
		if errors.Is(got, ErrDeadlineExceeded) != c.deadline || !errors.Is(got, c.err) {
			t.Errorf("%s: %v", c.name, got)
		}
		if c.deadline && connectionLost(got) {
			t.Errorf("%s: a deadline is never a lost connection", c.name)
		}
	}
}
