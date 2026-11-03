// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project.
//
// The resource server against a provider served by httptest, and the rate
// limiter: no database needed.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// fakeProvider serves discovery and a key set, and counts key set fetches.
type fakeProvider struct {
	server  *httptest.Server
	keys    atomic.Pointer[jose.JSONWebKeySet]
	fetches atomic.Int32
	down    atomic.Bool // the key set answers 503
}

func newFakeProvider(t *testing.T, keys ...testKey) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.setKeys(keys...)
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, 200, map[string]string{"issuer": p.server.URL, "jwks_uri": p.server.URL + "/jwks",
				"authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token"})
		case "/jwks":
			p.fetches.Add(1)
			if p.down.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, 200, p.keys.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeProvider) setKeys(keys ...testKey) {
	set := &jose.JSONWebKeySet{}
	for _, k := range keys {
		set.Keys = append(set.Keys, k.public())
	}
	p.keys.Store(set)
}

const testResource = "http://127.0.0.1:8080/mcp"

func accessClaims(issuer string, changes map[string]any) map[string]any {
	now := time.Now().Unix()
	claims := map[string]any{"iss": issuer, "sub": "u1", "aud": testResource, "client_id": "agent", "iat": now, "exp": now + 300}
	for k, v := range changes {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	return claims
}

func newTestResourceServer(t *testing.T, issuer string) *ResourceServer {
	t.Helper()
	rs, err := NewResourceServer(testResource, []string{issuer}, []string{"openid"}, "web", &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestMetadataAndChallenge(t *testing.T) {
	rs := newTestResourceServer(t, "https://idp.example")
	if !rs.MetadataPaths["/.well-known/oauth-protected-resource"] || !rs.MetadataPaths["/.well-known/oauth-protected-resource/mcp"] {
		t.Errorf("paths %v", rs.MetadataPaths)
	}
	if rs.MetadataURL != "http://127.0.0.1:8080/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("url %s", rs.MetadataURL)
	}
	if got := rs.Challenge(""); got != `Bearer resource_metadata="`+rs.MetadataURL+`", scope="openid"` {
		t.Errorf("challenge %s", got)
	}
	if !strings.HasSuffix(rs.Challenge("invalid_token"), `, error="invalid_token"`) {
		t.Errorf("challenge %s", rs.Challenge("invalid_token"))
	}
	resp := serve(&Gateway{Resource: rs}, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil))
	var meta map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	if resp.StatusCode != 200 || meta["resource"] != testResource {
		t.Errorf("metadata %d %v", resp.StatusCode, meta)
	}
}

func TestResourceConfigurationIsChecked(t *testing.T) {
	for resource, servers := range map[string][]string{
		"http://mcp.example/mcp":      {"https://idp.example"},
		"https://mcp.example/mcp?x=1": {"https://idp.example"},
		"https://mcp.example/mcp":     nil,
		"https://mcp.example/mcp#f":   {"https://idp.example"},
		"https://mcp.example/mcp ":    {"http://idp.example"},
	} {
		if _, err := NewResourceServer(resource, servers, nil, "", nil); err == nil {
			t.Errorf("%q %v: accepted", resource, servers)
		}
	}
}

func TestAValidTokenProvesIssuerSubjectAndClient(t *testing.T) {
	key := newTestKey(t, "k1")
	idp := newFakeProvider(t, key)
	rs := newTestResourceServer(t, idp.server.URL)
	id, err := rs.Verify(t.Context(), "Bearer "+key.sign(t, accessClaims(idp.server.URL, nil)))
	if err != nil || id != (Identity{idp.server.URL, "u1", "agent"}) {
		t.Fatalf("%+v %v", id, err)
	}
	if string(id.Context()) != `{"client":{"clientId":"agent","issuer":"`+idp.server.URL+`","subject":"u1"}}` {
		t.Errorf("context %s", id.Context())
	}
	for name, changes := range map[string]map[string]any{
		"azp in place of client_id": {"client_id": nil, "azp": "agent"},
		"an upper-case scheme":      {"aud": "HTTP://127.0.0.1:8080/mcp"},
		"one audience in a list":    {"aud": []string{testResource}},
	} {
		if _, err := rs.Verify(t.Context(), "Bearer "+key.sign(t, accessClaims(idp.server.URL, changes))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNoTokenIs401WithoutAnErrorCode(t *testing.T) {
	rs := newTestResourceServer(t, "https://idp.example")
	var rejected *TokenRejectedError
	if _, err := rs.Verify(t.Context(), ""); !errors.As(err, &rejected) || rejected.Code != "" {
		t.Errorf("%v", err)
	}
}

func TestCheapRefusalsNeverFetchAKey(t *testing.T) {
	key := newTestKey(t, "k1")
	idp := newFakeProvider(t, key)
	rs := newTestResourceServer(t, idp.server.URL)
	for name, authorization := range map[string]string{
		"not bearer":       "Basic abc",
		"not a JWT":        "Bearer not-a-jwt",
		"too long":         "Bearer " + strings.Repeat("a", maxTokenLength+1),
		"another issuer":   "Bearer " + key.sign(t, accessClaims("https://other.example", nil)),
		"another audience": "Bearer " + key.sign(t, accessClaims(idp.server.URL, map[string]any{"aud": "https://other.example/mcp"})),
		"two audiences":    "Bearer " + key.sign(t, accessClaims(idp.server.URL, map[string]any{"aud": []string{testResource, "https://other.example"}})),
	} {
		var rejected *TokenRejectedError
		if _, err := rs.Verify(t.Context(), authorization); !errors.As(err, &rejected) || rejected.Code != "invalid_token" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := idp.fetches.Load(); n != 0 {
		t.Errorf("%d key fetches", n)
	}
}

func TestSignatureAndClaims(t *testing.T) {
	key := newTestKey(t, "k1")
	idp := newFakeProvider(t, key)
	rs := newTestResourceServer(t, idp.server.URL)
	now := time.Now().Unix()
	for name, token := range map[string]string{
		"forged":            newTestKey(t, "k1").sign(t, accessClaims(idp.server.URL, nil)),
		"expired":           key.sign(t, accessClaims(idp.server.URL, map[string]any{"iat": now - 7200, "exp": now - 3600})),
		"no iat":            key.sign(t, accessClaims(idp.server.URL, map[string]any{"iat": nil})),
		"not before":        key.sign(t, accessClaims(idp.server.URL, map[string]any{"nbf": now + 3600})),
		"the web login":     key.sign(t, accessClaims(idp.server.URL, map[string]any{"client_id": "web"})),
		"no client id":      key.sign(t, accessClaims(idp.server.URL, map[string]any{"client_id": nil})),
		"no sub":            key.sign(t, accessClaims(idp.server.URL, map[string]any{"sub": nil})),
		"an unknown key id": newTestKey(t, "k9").sign(t, accessClaims(idp.server.URL, nil)),
	} {
		var rejected *TokenRejectedError
		if _, err := rs.Verify(t.Context(), "Bearer "+token); !errors.As(err, &rejected) || rejected.Code != "invalid_token" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAnUnknownKeyRefetchesAtMostOncePerCooldown(t *testing.T) {
	old := newTestKey(t, "k1")
	idp := newFakeProvider(t, old)
	rs := newTestResourceServer(t, idp.server.URL)
	if _, err := rs.Verify(t.Context(), "Bearer "+old.sign(t, accessClaims(idp.server.URL, nil))); err != nil {
		t.Fatal(err)
	}
	rotated := newTestKey(t, "k2")
	idp.setKeys(old, rotated)
	if _, err := rs.Verify(t.Context(), "Bearer "+rotated.sign(t, accessClaims(idp.server.URL, nil))); err != nil {
		t.Errorf("a rotated key is fetched: %v", err)
	}
	fetched := idp.fetches.Load()
	for range 5 {
		_, _ = rs.Verify(t.Context(), "Bearer "+newTestKey(t, "forged-"+rand8()).sign(t, accessClaims(idp.server.URL, nil)))
	}
	if n := idp.fetches.Load() - fetched; n != 0 {
		t.Errorf("forged key ids within the cooldown fetched %d times", n)
	}
}

func rand8() string { return strings.ToLower(randomToken()[:8]) }

func TestAnUnreachableProviderIsNotABadToken(t *testing.T) {
	key := newTestKey(t, "k1")
	idp := newFakeProvider(t, key)
	issuer := idp.server.URL
	rs := newTestResourceServer(t, issuer)
	idp.server.Close()
	if _, err := rs.Verify(t.Context(), "Bearer "+key.sign(t, accessClaims(issuer, nil))); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("%v", err)
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRateLimiter(60, 10)
	l.now = func() time.Time { return now }
	for i := range 60 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d refused inside the burst", i)
		}
	}
	if ok, wait := l.Allow("a"); ok || wait != 1 {
		t.Errorf("over the limit: %v, Retry-After %d", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("each key has its own bucket")
	}
	now = now.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("one token a second at 60 a minute")
	}

	var off *RateLimiter
	if ok, _ := off.Allow("a"); !ok {
		t.Error("nil is off")
	}
	if ok, _ := NewRateLimiter(0, 10).Allow("a"); !ok {
		t.Error("0 is off")
	}
}

func TestRateLimiterMemoryIsBounded(t *testing.T) {
	l := NewRateLimiter(1, 3)
	for _, k := range []string{"a", "b", "c", "d"} {
		l.Allow(k)
	}
	if len(l.buckets) != 3 || l.buckets["a"] != nil {
		t.Errorf("buckets %v", l.buckets)
	}
}

func TestConcurrentRequestsNeverExceedTheLimit(t *testing.T) {
	l := NewRateLimiter(100, 10)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 400 {
		wg.Go(func() {
			if ok, _ := l.Allow("k"); ok {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n < 100 || n > 101 {
		t.Errorf("%d allowed, want 100", n)
	}
}
