// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project: it is a baseline to learn from,
// not a library. The rules it keeps are in ../PROTOCOL.md ("Sign-in",
// "Identity") and in docs/advanced/LOGIN-TRANSPORT.md of the pgmi repository.
//
// Sign-in in front of the database:
//
//	GET  /auth/login?returnTo=/path  start the authorization code flow (PKCE)
//	GET  /auth/callback              check state, exchange the code, verify the
//	                                 ID token, call api.open_login_session
//	POST /auth/logout                api.close_login_session, clear the cookies
//	POST /auth/dev-login             DEV_LOGIN=1 only: api.open_dev_login_session
//
// Only the opaque login-session handle reaches the browser, in an HttpOnly
// cookie. The database decides who the caller is; nothing here names a person.
//
// github.com/coreos/go-oidc does discovery (and checks that it names the
// configured issuer), the JWKS cache and the signature, issuer and audience
// checks; golang.org/x/oauth2 builds the authorization URL and exchanges the
// code with PKCE. The rest is checked by hand below, because the library
// leaves it to the caller: the nonce, azp when there are several audiences,
// iat, and exp/nbf with this gateway's 60 s of leeway.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ADAPT: cookie names, lifetimes and the development issuer seeded by
// membership/dev/01-issuers.sql.
const (
	sessionCookie = "__Host-session"
	csrfCookie    = "__Host-csrf"
	loginCookie   = "__Host-login"
	sessionMaxAge = 7 * 24 * 3600
	loginMaxAge   = 600
	devIssuer     = "dev"
	clockSkew     = 60 * time.Second
)

// idTokenAlgorithms: never "none", never an HMAC (a public key is not a secret).
var idTokenAlgorithms = []string{oidc.RS256, oidc.PS256, oidc.ES256, oidc.EdDSA}

// Sessions issues and checks the cookies of a login session.
type Sessions struct {
	CSRFSecret []byte // the same on every gateway process
}

// CSRFToken is the value of the readable csrf cookie for one login session:
// an HMAC a cross-site page cannot compute or read.
func (s Sessions) CSRFToken(loginSession string) string {
	mac := hmac.New(sha256.New, s.CSRFSecret)
	mac.Write([]byte("csrf\x00" + loginSession))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CSRFOK reports whether a request riding on the session cookie may change state.
func (s Sessions) CSRFOK(method, loginSession, presented string) bool {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || loginSession == "" {
		return true
	}
	return presented != "" && hmac.Equal([]byte(s.CSRFToken(loginSession)), []byte(presented))
}

func (s Sessions) set(w http.ResponseWriter, loginSession string) {
	setCookie(w, sessionCookie, loginSession, sessionMaxAge, true)
	setCookie(w, csrfCookie, s.CSRFToken(loginSession), sessionMaxAge, false)
}

func cookieValue(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

// setCookie: __Host- names need Secure, Path=/ and no Domain. maxAge -1
// clears the cookie (Max-Age=0).
func setCookie(w http.ResponseWriter, name, value string, maxAge int, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: httpOnly, SameSite: http.SameSiteLaxMode})
}

func csrfRefused() *problem {
	return &problem{status: http.StatusForbidden, title: "Forbidden", code: "pgmi.csrf_refused",
		detail: "send the " + csrfCookie + " value in X-CSRF-Token"}
}

func loginFailed(status int, code, detail string) *problem {
	return &problem{status: status, title: "Sign-in failed", code: code, detail: detail}
}

// -- The database side of signing in ------------------------------------------

// openLoginSession calls api.open_login_session with verified claims and
// closes the handle the browser held before: each sign-in gets a new one.
// The token is "" and refused names the problem code when the database says no.
func (p *Postgres) openLoginSession(ctx context.Context, claims loginClaims, previous string) (token, refused string, err error) {
	conn, err := p.acquire(ctx)
	if err != nil {
		return "", "", err
	}
	defer conn.Release()
	var t, problemCode *string
	if err := conn.QueryRow(ctx, "SELECT token, problem FROM api.open_login_session($1, $2, $3, $4, $5, $6)",
		claims.Issuer, claims.Subject, claims.Email, claims.Name, claims.EmailVerified, claims.AMR).Scan(&t, &problemCode); err != nil {
		return "", "", fmt.Errorf("open login session: %w", err)
	}
	if t == nil {
		return "", deref(problemCode), nil
	}
	if previous != "" {
		if _, err := conn.Exec(ctx, "SELECT api.close_login_session($1)", previous); err != nil {
			return "", "", fmt.Errorf("close previous login session: %w", err)
		}
	}
	return *t, "", nil
}

// openDevLoginSession: "" when the issuer is not a development one.
func (p *Postgres) openDevLoginSession(ctx context.Context, subject, email string, displayName *string, previous string) (string, error) {
	conn, err := p.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()
	var token *string
	if err := conn.QueryRow(ctx, "SELECT api.open_dev_login_session($1, $2, $3, $4)",
		devIssuer, subject, email, displayName).Scan(&token); err != nil {
		return "", fmt.Errorf("open dev login session: %w", err)
	}
	if token == nil {
		return "", nil
	}
	if previous != "" {
		if _, err := conn.Exec(ctx, "SELECT api.close_login_session($1)", previous); err != nil {
			return "", fmt.Errorf("close previous login session: %w", err)
		}
	}
	return *token, nil
}

func (p *Postgres) closeLoginSession(ctx context.Context, loginSession string) error {
	conn, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT api.close_login_session($1)", loginSession); err != nil {
		return fmt.Errorf("close login session: %w", err)
	}
	return nil
}

// -- Dev sign-in and logout --------------------------------------------------------

// devLogin signs in without a provider. 404 unless DEV_LOGIN=1 (the route is
// not registered); the database has the function only in an env=dev deploy,
// so both must agree.
func (g *Gateway) devLogin(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Subject     *string `json:"subject"`
		Email       *string `json:"email"`
		DisplayName *string `json:"displayName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&in); err != nil || in.Subject == nil || in.Email == nil {
		return &problem{status: http.StatusBadRequest, title: "Bad Request", code: "login.request_invalid",
			detail: `send {"subject", "email", "displayName"} as JSON strings`}
	}
	token, err := g.DB.openDevLoginSession(r.Context(), *in.Subject, *in.Email, in.DisplayName, cookieValue(r, sessionCookie))
	if err != nil {
		return err
	}
	if token == "" {
		return loginFailed(http.StatusForbidden, "login.refused", "the database refused the sign-in")
	}
	g.Sessions.set(w, token)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (g *Gateway) logout(w http.ResponseWriter, r *http.Request) error {
	loginSession := cookieValue(r, sessionCookie)
	if !g.Sessions.CSRFOK(r.Method, loginSession, r.Header.Get("X-CSRF-Token")) {
		return csrfRefused()
	}
	if loginSession != "" {
		if err := g.DB.closeLoginSession(r.Context(), loginSession); err != nil {
			return err
		}
	}
	setCookie(w, sessionCookie, "", -1, true)
	setCookie(w, csrfCookie, "", -1, false)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// -- OIDC ----------------------------------------------------------------------------

// OIDCConfig is the OIDC_* environment.
type OIDCConfig struct {
	Issuer       string // exactly as listed in the oidc_issuers deploy parameter
	ClientID     string
	ClientSecret string // confidential clients only (client_secret_basic)
	RedirectURI  string // this gateway's /auth/callback, as registered
	Scopes       []string
}

// OIDCLogin is one issuer's authorization code flow.
type OIDCLogin struct {
	config  OIDCConfig
	client  *http.Client
	pending *pendingLogins

	mu       sync.Mutex    // guards provider
	provider *oidcProvider // nil until discovery succeeded
}

// oidcProvider is what discovery yields: the endpoints, the issuer's key set
// and a verifier that reads it.
type oidcProvider struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
	keys     *issuerKeys
}

// NewOIDCLogin checks the configuration; discovery happens at the first
// sign-in, so a provider that is down at startup does not stop the gateway.
func NewOIDCLogin(config OIDCConfig, client *http.Client) (*OIDCLogin, error) {
	if config.ClientID == "" || config.RedirectURI == "" {
		return nil, errors.New("OIDC_ISSUER is set but OIDC_CLIENT_ID or OIDC_REDIRECT_URI is not")
	}
	if err := checkIssuerURL(config.Issuer, "OIDC_ISSUER"); err != nil {
		return nil, err
	}
	return &OIDCLogin{config: config, client: client, pending: newPendingLogins(loginMaxAge*time.Second, 10000)}, nil
}

// checkIssuerURL: https, or http on a loopback host (a development provider).
func checkIssuerURL(value, name string) error {
	u, err := url.Parse(value)
	secure := err == nil && (u.Scheme == "https" || u.Scheme == "http" && isLoopback(u.Hostname()))
	if !secure {
		return fmt.Errorf("%s must be https (http only on a loopback host): %q", name, value)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// discover reads the provider's metadata once. go-oidc refuses a discovery
// document that names another issuer (an attacker's metadata would bring its
// own keys).
func (o *OIDCLogin) discover(ctx context.Context) (*oidcProvider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, o.client), o.config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", o.config.Issuer, err)
	}
	var meta struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := provider.Claims(&meta); err != nil || meta.JWKSURI == "" {
		return nil, fmt.Errorf("discover %s: no jwks_uri", o.config.Issuer)
	}
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	if o.config.ClientSecret != "" {
		endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	// The key set is the resource server's cache (resource.go) with no
	// cooldown: an unknown kid refetches at once, so after a key rotation the
	// next sign-in is not refused. Safe here, because ID tokens reach this
	// gateway only from the token endpoint, never from a client.
	keys := &issuerKeys{issuer: o.config.Issuer, client: o.client, jwksURI: meta.JWKSURI}
	o.provider = &oidcProvider{
		oauth: &oauth2.Config{ClientID: o.config.ClientID, ClientSecret: o.config.ClientSecret,
			Endpoint: endpoint, RedirectURL: o.config.RedirectURI, Scopes: o.config.Scopes},
		verifier: oidc.NewVerifier(o.config.Issuer, keySet{keys}, &oidc.Config{ClientID: o.config.ClientID,
			SupportedSigningAlgs: idTokenAlgorithms, SkipExpiryCheck: true}), // exp is checked below, with leeway
		keys: keys,
	}
	return o.provider, nil
}

type pendingLogin struct {
	state, nonce, verifier, returnTo string
}

func randomToken() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(32))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
	return b
}

// login starts a sign-in: state, nonce and the PKCE verifier stay on this
// gateway, keyed by a random value in the __Host-login cookie.
func (g *Gateway) login(w http.ResponseWriter, r *http.Request) error {
	o := g.Login
	provider, err := o.discover(r.Context())
	if err != nil {
		g.Log.WarnContext(r.Context(), "sign-in: discovery failed", "error", err)
		return loginFailed(http.StatusBadGateway, "login.provider_unavailable", "the provider could not be reached")
	}
	attempt := pendingLogin{state: randomToken(), nonce: randomToken(), verifier: oauth2.GenerateVerifier(),
		returnTo: safeReturnTo(r.URL.Query().Get("returnTo"))}
	location := provider.oauth.AuthCodeURL(attempt.state, oidc.Nonce(attempt.nonce), oauth2.S256ChallengeOption(attempt.verifier))
	setCookie(w, loginCookie, o.pending.put(attempt), loginMaxAge, true)
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
	return nil
}

// callback finishes a sign-in. The pending sign-in is taken first, so a
// callback works once; a refused one clears the login cookie.
func (g *Gateway) callback(w http.ResponseWriter, r *http.Request) error {
	setCookie(w, loginCookie, "", -1, true)
	o := g.Login
	query := r.URL.Query()
	attempt, ok := o.pending.take(cookieValue(r, loginCookie))
	state := query.Get("state")
	if !ok || state == "" || !hmac.Equal([]byte(state), []byte(attempt.state)) {
		return loginFailed(http.StatusBadRequest, "login.state_invalid", "unknown, expired or replayed sign-in")
	}
	if e := query.Get("error"); e != "" {
		return loginFailed(http.StatusBadRequest, "login.provider_error", truncate(e, 100))
	}
	code := query.Get("code")
	if code == "" {
		return loginFailed(http.StatusBadRequest, "login.state_invalid", "the callback carries no code")
	}

	ctx := r.Context()
	provider, err := o.discover(ctx)
	if err != nil {
		g.Log.WarnContext(ctx, "sign-in: discovery failed", "error", err)
		return loginFailed(http.StatusBadGateway, "login.provider_unavailable", "the provider could not be reached")
	}
	tokens, err := provider.oauth.Exchange(oidc.ClientContext(ctx, o.client), code, oauth2.VerifierOption(attempt.verifier))
	if refused := (*oauth2.RetrieveError)(nil); errors.As(err, &refused) {
		return loginFailed(http.StatusUnauthorized, "login.provider_refused", fmt.Sprintf("the provider answered %d", refused.Response.StatusCode))
	}
	if err != nil {
		g.Log.WarnContext(ctx, "sign-in: code exchange failed", "error", err)
		return loginFailed(http.StatusBadGateway, "login.provider_unavailable", "the provider could not be reached")
	}
	raw, _ := tokens.Extra("id_token").(string)
	if raw == "" {
		return loginFailed(http.StatusUnauthorized, "login.token_invalid", "the provider returned no ID token")
	}
	// The signing key first: a provider that cannot serve its keys is 502,
	// never a bad token. The verifier then finds the key in the cache.
	if err := provider.keys.prefetch(ctx, raw); err != nil {
		g.Log.WarnContext(ctx, "sign-in: signing keys unavailable", "error", err)
		return loginFailed(http.StatusBadGateway, "login.provider_unavailable", "the provider could not be reached")
	}
	claims, err := verifyIDToken(ctx, provider.verifier, raw, o.config.ClientID, attempt.nonce, time.Now())
	if err != nil {
		g.Log.InfoContext(ctx, "sign-in refused", "reason", err)
		return loginFailed(http.StatusUnauthorized, "login.token_invalid", "the ID token is not valid")
	}

	token, refused, err := g.DB.openLoginSession(ctx, claims, cookieValue(r, sessionCookie))
	if err != nil {
		return err
	}
	if token == "" {
		return loginFailed(http.StatusForbidden, refused, "the database refused the sign-in")
	}
	g.Sessions.set(w, token)
	w.Header().Set("Location", attempt.returnTo)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// loginClaims are the api.open_login_session arguments. EmailVerified and AMR
// go through as the provider sent them; a value of the wrong JSON type becomes
// NULL, which the database reads as unverified and as no second factor.
type loginClaims struct {
	Issuer, Subject string
	Email, Name     *string
	EmailVerified   *bool
	AMR             []string
}

// verifyIDToken: go-oidc checks the signature (allowed algorithms only), iss
// and that aud contains this client. The rest is the caller's job.
func verifyIDToken(ctx context.Context, verifier *oidc.IDTokenVerifier, raw, clientID, nonce string, now time.Time) (loginClaims, error) {
	token, err := verifier.Verify(ctx, raw)
	if err != nil {
		return loginClaims{}, err
	}
	var c struct {
		Subject       any    `json:"sub"`
		Audience      any    `json:"aud"`
		AuthorizedBy  any    `json:"azp"`
		Expiry        *int64 `json:"exp"`
		IssuedAt      *int64 `json:"iat"`
		NotBefore     *int64 `json:"nbf"`
		Nonce         any    `json:"nonce"`
		Email         any    `json:"email"`
		Name          any    `json:"name"`
		EmailVerified any    `json:"email_verified"`
		AMR           any    `json:"amr"`
	}
	if err := token.Claims(&c); err != nil {
		return loginClaims{}, fmt.Errorf("claims: %w", err)
	}
	if err := checkTimes(c.Expiry, c.IssuedAt, c.NotBefore, now); err != nil {
		return loginClaims{}, err
	}
	// Several audiences: the token must say it was issued to this client.
	if audiences, ok := c.Audience.([]any); ok && len(audiences) > 1 && c.AuthorizedBy != clientID {
		return loginClaims{}, errors.New("azp does not name this client")
	}
	// The nonce ties the token to this browser's sign-in: a token stolen from
	// another sign-in carries another nonce.
	if got, ok := c.Nonce.(string); !ok || !hmac.Equal([]byte(got), []byte(nonce)) {
		return loginClaims{}, errors.New("nonce mismatch")
	}
	subject, ok := c.Subject.(string)
	if !ok || subject == "" {
		return loginClaims{}, errors.New("sub is not a string")
	}
	claims := loginClaims{Issuer: token.Issuer, Subject: subject}
	if s, ok := c.Email.(string); ok {
		claims.Email = &s
	}
	if s, ok := c.Name.(string); ok {
		claims.Name = &s
	}
	if b, ok := c.EmailVerified.(bool); ok {
		claims.EmailVerified = &b
	}
	if list, ok := c.AMR.([]any); ok {
		claims.AMR = []string{}
		for _, a := range list {
			s, ok := a.(string)
			if !ok {
				claims.AMR = nil
				break
			}
			claims.AMR = append(claims.AMR, s)
		}
	}
	return claims, nil
}

// checkTimes: exp and iat required, nbf when present, each with clockSkew of
// leeway for clocks that disagree a little.
func checkTimes(exp, iat, nbf *int64, now time.Time) error {
	switch {
	case exp == nil || iat == nil:
		return errors.New("exp and iat are required")
	case now.Add(-clockSkew).After(time.Unix(*exp, 0)):
		return errors.New("expired")
	case now.Add(clockSkew).Before(time.Unix(*iat, 0)):
		return errors.New("issued in the future")
	case nbf != nil && now.Add(clockSkew).Before(time.Unix(*nbf, 0)):
		return errors.New("not valid yet")
	}
	return nil
}

// safeReturnTo keeps the redirect on this site: a path, not "//host" (a
// scheme-relative URL) nor "/\host" (which browsers read the same), and only
// visible ASCII, so no header can be injected through it.
func safeReturnTo(value string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, `\`) {
		return "/"
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '!' || value[i] > '~' {
			return "/"
		}
	}
	return value
}

// pendingLogins holds sign-ins between /auth/login and /auth/callback, each
// usable once, for at most ttl, at most limit of them (the oldest goes first).
// Per process: behind a load balancer, pin /auth/* to one process or keep
// this in a shared store. ADAPT.
type pendingLogins struct {
	ttl   time.Duration
	limit int

	mu    sync.Mutex
	items map[string]pendingEntry
}

type pendingEntry struct {
	at      time.Time
	attempt pendingLogin
}

func newPendingLogins(ttl time.Duration, limit int) *pendingLogins {
	return &pendingLogins{ttl: ttl, limit: limit, items: map[string]pendingEntry{}}
}

func (p *pendingLogins) put(attempt pendingLogin) string {
	key := randomToken()
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.items {
		if now.Sub(e.at) > p.ttl {
			delete(p.items, k)
		}
	}
	for len(p.items) >= p.limit {
		oldest := ""
		for k, e := range p.items {
			if oldest == "" || e.at.Before(p.items[oldest].at) {
				oldest = k
			}
		}
		delete(p.items, oldest)
	}
	p.items[key] = pendingEntry{at: now, attempt: attempt}
	return key
}

func (p *pendingLogins) take(key string) (pendingLogin, bool) {
	if key == "" {
		return pendingLogin{}, false
	}
	p.mu.Lock()
	e, ok := p.items[key]
	delete(p.items, key)
	p.mu.Unlock()
	if !ok || time.Since(e.at) > p.ttl {
		return pendingLogin{}, false
	}
	return e.attempt, true
}
