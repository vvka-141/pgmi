// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../PROTOCOL.md ("MCP clients with
// OAuth", "Rate limits").
//
// The OAuth 2.1 resource server in front of /mcp, and the rate limits.
//
// An MCP client signs in at the identity provider and sends
// `Authorization: Bearer <access token>` on every request. The token is
// checked here, against the provider's published keys, and the database gets
// only what it proved: the issuer, the subject and the client id. The token
// itself is never forwarded and never logged. The database decides who that
// is and whether the client may act yet.
//
//	GET /.well-known/oauth-protected-resource[/mcp]  RFC 9728 metadata: where
//	                                                  to get a token for this server
//	POST /mcp without a valid token                   401 with
//	                                                  WWW-Authenticate: Bearer resource_metadata="..."
//
// Access tokens come from any client, unlike ID tokens, so the checks run
// cheapest first: a token that is not a JWT, names an issuer not configured
// or another audience is refused before any key is fetched, and an unknown
// key id refetches the key set at most once per unknownKeyCooldown.
package main

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
)

const (
	maxTokenLength     = 8192
	providerTimeout    = 5 * time.Second
	keySetLifetime     = 300 * time.Second
	unknownKeyCooldown = 10 * time.Second // access tokens: a rotated key is picked up within this long
)

var tokenAlgorithms = []jose.SignatureAlgorithm{jose.RS256, jose.PS256, jose.ES256, jose.EdDSA}

// TokenRejectedError is 401. Code is "invalid_token", or "" when no token
// was sent: RFC 6750 §3.1 sends no error code to a request without credentials.
type TokenRejectedError struct {
	Code, Reason string
}

func (e *TokenRejectedError) Error() string { return "token rejected: " + e.Reason }

// ErrProviderUnavailable is 503: the provider's discovery or keys could not be read.
var ErrProviderUnavailable = errors.New("identity provider unavailable")

// Identity is what a valid access token proves.
type Identity struct {
	Issuer, Subject, ClientID string
}

// Context is the MCP context api.mcp_handle_request resolves, and nothing else.
func (id Identity) Context() json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"client": map[string]string{
		"issuer": id.Issuer, "subject": id.Subject, "clientId": id.ClientID}})
	return raw
}

// ResourceServer is the MCP_RESOURCE, MCP_AUTHORIZATION_SERVERS and
// MCP_SCOPES environment, and the key sets of those issuers.
type ResourceServer struct {
	Resource      string
	Scopes        []string
	MetadataPaths map[string]bool
	MetadataURL   string

	issuers []string
	keys    map[string]*issuerKeys
	// ADAPT: every client id that is not an MCP client. A token issued to the
	// web sign-in is a person's browser session, never an agent.
	refusedClients map[string]bool
}

func NewResourceServer(resource string, issuers, scopes []string, loginClientID string, client *http.Client) (*ResourceServer, error) {
	if err := checkResourceURL(resource, "MCP_RESOURCE"); err != nil {
		return nil, err
	}
	if len(issuers) == 0 {
		return nil, errors.New("MCP_RESOURCE is set but MCP_AUTHORIZATION_SERVERS is not")
	}
	rs := &ResourceServer{Resource: resource, Scopes: scopes, issuers: issuers,
		keys: map[string]*issuerKeys{}, refusedClients: map[string]bool{}}
	for _, issuer := range issuers {
		if err := checkResourceURL(issuer, "MCP_AUTHORIZATION_SERVERS"); err != nil {
			return nil, err
		}
		rs.keys[issuer] = &issuerKeys{issuer: issuer, client: client, cooldown: unknownKeyCooldown}
	}
	if loginClientID != "" {
		rs.refusedClients[loginClientID] = true
	}
	u, _ := url.Parse(resource)
	path := "/.well-known/oauth-protected-resource" + strings.TrimSuffix(u.Path, "/")
	rs.MetadataPaths = map[string]bool{"/.well-known/oauth-protected-resource": true, path: true}
	rs.MetadataURL = u.Scheme + "://" + u.Host + path
	return rs, nil
}

func checkResourceURL(value, name string) error {
	if err := checkIssuerURL(value, name); err != nil {
		return err
	}
	if u, _ := url.Parse(value); u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s must have no query and no fragment: %q", name, value)
	}
	return nil
}

// Metadata is RFC 9728 §2; the MCP spec requires authorization_servers.
func (rs *ResourceServer) Metadata() map[string]any {
	return map[string]any{"resource": rs.Resource, "authorization_servers": rs.issuers,
		"bearer_methods_supported": []string{"header"}, "scopes_supported": rs.Scopes}
}

// Challenge is the WWW-Authenticate value of a 401 (RFC 9728 §5.1).
func (rs *ResourceServer) Challenge(code string) string {
	value := fmt.Sprintf(`Bearer resource_metadata="%s"`, rs.MetadataURL)
	if len(rs.Scopes) > 0 {
		value += fmt.Sprintf(`, scope="%s"`, strings.Join(rs.Scopes, " "))
	}
	if code != "" {
		value += fmt.Sprintf(`, error="%s"`, code)
	}
	return value
}

func rejected(reason string) error { return &TokenRejectedError{Code: "invalid_token", Reason: reason} }

// Verify returns the identity a valid access token proves, a
// *TokenRejectedError or ErrProviderUnavailable.
func (rs *ResourceServer) Verify(ctx context.Context, authorization string) (Identity, error) {
	if authorization == "" {
		return Identity{}, &TokenRejectedError{Reason: "no token"}
	}
	scheme, token, _ := strings.Cut(authorization, " ")
	token = strings.TrimSpace(token)
	if !strings.EqualFold(scheme, "bearer") || token == "" || len(token) > maxTokenLength {
		return Identity{}, rejected("not a bearer token")
	}
	// Parsing checks the algorithm against the allowed list: never "none",
	// never an HMAC keyed with a public key.
	jws, err := jose.ParseSignedCompact(token, tokenAlgorithms)
	if err != nil || len(jws.Signatures) != 1 {
		return Identity{}, rejected("not a JWT with an accepted algorithm")
	}
	var unverified struct {
		Issuer   string `json:"iss"`
		Audience any    `json:"aud"`
	}
	if json.Unmarshal(jws.UnsafePayloadWithoutVerification(), &unverified) != nil {
		return Identity{}, rejected("not a JWT")
	}
	keys := rs.keys[unverified.Issuer]
	kid := jws.Signatures[0].Header.KeyID
	switch {
	case keys == nil:
		return Identity{}, rejected("issuer not accepted")
	case kid == "":
		return Identity{}, rejected("no key id")
	case !rs.isAudience(unverified.Audience):
		return Identity{}, rejected("audience is not this resource")
	}

	key, err := keys.key(ctx, kid)
	if err != nil {
		return Identity{}, err
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return Identity{}, rejected("bad signature")
	}
	var claims struct {
		Issuer    any    `json:"iss"`
		Subject   any    `json:"sub"`
		Audience  any    `json:"aud"`
		ClientID  any    `json:"client_id"`
		AZP       any    `json:"azp"`
		Expiry    *int64 `json:"exp"`
		IssuedAt  *int64 `json:"iat"`
		NotBefore *int64 `json:"nbf"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, rejected("claims")
	}
	if claims.Issuer != keys.issuer {
		return Identity{}, rejected("issuer")
	}
	if err := checkTimes(claims.Expiry, claims.IssuedAt, claims.NotBefore, time.Now()); err != nil {
		return Identity{}, rejected(err.Error())
	}
	// RFC 8707: issued for this resource and only this one. A token that also
	// names another audience could be replayed here by that other server.
	if !rs.isAudience(claims.Audience) {
		return Identity{}, rejected("audience is not this resource")
	}
	clientID, _ := claims.ClientID.(string)
	if claims.ClientID == nil {
		clientID, _ = claims.AZP.(string)
	}
	if clientID == "" || rs.refusedClients[clientID] {
		return Identity{}, rejected("no MCP client id")
	}
	subject, _ := claims.Subject.(string)
	if subject == "" {
		return Identity{}, rejected("sub is not a string")
	}
	return Identity{Issuer: keys.issuer, Subject: subject, ClientID: clientID}, nil
}

// isAudience: exactly one audience, this resource; scheme and host compared
// case-insensitively, as the MCP spec asks.
func (rs *ResourceServer) isAudience(audience any) bool {
	if list, ok := audience.([]any); ok && len(list) == 1 {
		audience = list[0]
	}
	s, ok := audience.(string)
	return ok && canonicalURI(s) == canonicalURI(rs.Resource)
}

func canonicalURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	return u.String()
}

// issuerKeys is one authorization server's key set: discovery once (unless
// jwksURI is given), then the JWKS, cached for keySetLifetime. An unknown key
// id refetches the set at most once per cooldown.
type issuerKeys struct {
	issuer   string
	client   *http.Client
	cooldown time.Duration

	mu        sync.Mutex // held across a fetch: one fetch per issuer at a time
	jwksURI   string
	set       jose.JSONWebKeySet
	fetched   time.Time
	refetched time.Time // the last refetch for an unknown kid
}

func (k *issuerKeys) key(ctx context.Context, kid string) (jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.jwksURI == "" {
		provider, err := oidc.NewProvider(oidc.ClientContext(ctx, k.client), k.issuer)
		if err != nil {
			return jose.JSONWebKey{}, fmt.Errorf("%w: discovery: %v", ErrProviderUnavailable, err)
		}
		var meta struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if err := provider.Claims(&meta); err != nil || meta.JWKSURI == "" {
			return jose.JSONWebKey{}, fmt.Errorf("%w: discovery names no jwks_uri", ErrProviderUnavailable)
		}
		k.jwksURI = meta.JWKSURI
	}
	if time.Since(k.fetched) > keySetLifetime {
		if err := k.fetch(ctx); err != nil {
			return jose.JSONWebKey{}, err
		}
	}
	if key, ok := k.lookup(kid); ok {
		return key, nil
	}
	if time.Since(k.refetched) < k.cooldown {
		return jose.JSONWebKey{}, rejected("unknown key")
	}
	k.refetched = time.Now()
	if err := k.fetch(ctx); err != nil {
		return jose.JSONWebKey{}, err
	}
	if key, ok := k.lookup(kid); ok {
		return key, nil
	}
	return jose.JSONWebKey{}, rejected("unknown key")
}

// lookup finds a key by id; a token without one may use the only key there is.
func (k *issuerKeys) lookup(kid string) (jose.JSONWebKey, bool) {
	if kid == "" && len(k.set.Keys) == 1 {
		return k.set.Keys[0], true
	}
	found := k.set.Key(kid)
	if kid == "" || len(found) == 0 {
		return jose.JSONWebKey{}, false
	}
	return found[0], true
}

// prefetch loads the key a token names, so that only a provider that cannot
// serve its keys is an error here; any fault of the token is left to the
// verifier.
func (k *issuerKeys) prefetch(ctx context.Context, token string) error {
	jws, err := jose.ParseSignedCompact(token, tokenAlgorithms)
	if err != nil || len(jws.Signatures) != 1 {
		return nil
	}
	if _, err := k.key(ctx, jws.Signatures[0].Header.KeyID); errors.Is(err, ErrProviderUnavailable) {
		return err
	}
	return nil
}

// keySet lets go-oidc's verifier read an issuerKeys cache (oidc.KeySet).
type keySet struct{ keys *issuerKeys }

func (s keySet) VerifySignature(ctx context.Context, token string) ([]byte, error) {
	jws, err := jose.ParseSignedCompact(token, tokenAlgorithms)
	if err != nil || len(jws.Signatures) != 1 {
		return nil, errors.New("not a JWS with an accepted algorithm")
	}
	key, err := s.keys.key(ctx, jws.Signatures[0].Header.KeyID)
	if err != nil {
		return nil, err
	}
	return jws.Verify(key)
}

func (k *issuerKeys) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.jwksURI, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	var set jose.JSONWebKeySet
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&set) != nil {
		return fmt.Errorf("%w: key set answered %d", ErrProviderUnavailable, resp.StatusCode)
	}
	k.set, k.fetched = set, time.Now()
	return nil
}

// RateLimiter is a token bucket per key, refilled continuously to perMinute a
// minute. Per process: N gateway processes allow N times the limit. Safe for
// concurrent use behind one lock; memory is bounded by maxKeys, the least
// recently seen key going first (a key that comes back starts full, which only
// an attacker holding more than maxKeys addresses gains from). A nil
// *RateLimiter, or perMinute 0, allows everything.
//
// ADAPT: behind a reverse proxy the client address is the proxy's; key on
// the address your proxy forwards, from a header only it can set, or limit at
// the proxy and set RATE_LIMIT_PER_IP=0 here.
type RateLimiter struct {
	perMinute int
	maxKeys   int
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*list.Element // of *bucket, most recently seen at the front
	order   *list.List
}

type bucket struct {
	key    string
	tokens float64
	seen   time.Time
}

func NewRateLimiter(perMinute, maxKeys int) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, maxKeys: maxKeys, now: time.Now,
		buckets: map[string]*list.Element{}, order: list.New()}
}

// Allow reports whether one more request under key fits, and if not, how
// many seconds until one does.
func (l *RateLimiter) Allow(key string) (bool, int) {
	if l == nil || l.perMinute <= 0 {
		return true, 0
	}
	rate := float64(l.perMinute) / 60
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	element, ok := l.buckets[key]
	if !ok {
		element = l.order.PushFront(&bucket{key: key, tokens: float64(l.perMinute), seen: now})
		l.buckets[key] = element
		for l.order.Len() > l.maxKeys {
			delete(l.buckets, l.order.Remove(l.order.Back()).(*bucket).key)
		}
	}
	l.order.MoveToFront(element)
	b := element.Value.(*bucket)
	b.tokens = min(float64(l.perMinute), b.tokens+now.Sub(b.seen).Seconds()*rate)
	b.seen = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, max(1, int(math.Ceil((1-b.tokens)/rate)))
}
