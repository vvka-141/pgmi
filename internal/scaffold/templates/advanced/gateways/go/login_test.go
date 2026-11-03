// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own that fits this project.
//
// The sign-in checks the library leaves to the caller, against tokens signed
// here: no database and no identity provider needed.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
)

const testIssuer = "https://idp.example"

// testKey signs tokens; testKeyID names it in the key set.
type testKey struct {
	private *rsa.PrivateKey
	kid     string
}

func newTestKey(t *testing.T, kid string) testKey {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{private, kid}
}

func (k testKey) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: k.private, KeyID: k.kid}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (k testKey) public() jose.JSONWebKey {
	return jose.JSONWebKey{Key: k.private.Public(), KeyID: k.kid, Algorithm: "RS256", Use: "sig"}
}

func idClaims(changes map[string]any) map[string]any {
	now := time.Now().Unix()
	claims := map[string]any{"iss": testIssuer, "sub": "u1", "aud": "web", "iat": now, "exp": now + 300,
		"nonce": "n1", "email": "u1@example.com", "email_verified": true, "amr": []string{"pwd", "mfa"}}
	for k, v := range changes {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	return claims
}

func TestVerifyIDToken(t *testing.T) {
	key := newTestKey(t, "k1")
	verifier := oidc.NewVerifier(testIssuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{key.private.Public()}},
		&oidc.Config{ClientID: "web", SupportedSigningAlgs: idTokenAlgorithms, SkipExpiryCheck: true})
	verify := func(changes map[string]any) (loginClaims, error) {
		return verifyIDToken(t.Context(), verifier, key.sign(t, idClaims(changes)), "web", "n1", time.Now())
	}

	claims, err := verify(nil)
	if err != nil || claims.Subject != "u1" || *claims.Email != "u1@example.com" || !*claims.EmailVerified || len(claims.AMR) != 2 {
		t.Fatalf("a valid token: %+v %v", claims, err)
	}
	if _, err := verify(map[string]any{"aud": []string{"web", "other"}, "azp": "web"}); err != nil {
		t.Errorf("several audiences with azp naming this client: %v", err)
	}
	if _, err := verify(map[string]any{"exp": time.Now().Unix() - 30}); err != nil {
		t.Errorf("expired within the leeway: %v", err)
	}

	now := time.Now().Unix()
	for name, changes := range map[string]map[string]any{
		"wrong nonce":                {"nonce": "other"},
		"no nonce":                   {"nonce": nil},
		"nonce not a string":         {"nonce": 5},
		"no iat":                     {"iat": nil},
		"iat in the future":          {"iat": now + 3600},
		"no exp":                     {"exp": nil},
		"expired":                    {"exp": now - 3600, "iat": now - 7200},
		"not before":                 {"nbf": now + 3600},
		"another audience":           {"aud": "other"},
		"several audiences, no azp":  {"aud": []string{"web", "other"}},
		"several audiences, foreign": {"aud": []string{"web", "other"}, "azp": "other"},
		"another issuer":             {"iss": "https://evil.example"},
		"no sub":                     {"sub": nil},
		"sub not a string":           {"sub": 5},
	} {
		if _, err := verify(changes); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if claims, err := verify(map[string]any{"email_verified": "true", "amr": "mfa"}); err != nil || claims.EmailVerified != nil || claims.AMR != nil {
		t.Errorf("a claim of the wrong type is NULL: %+v %v", claims, err)
	}
	if claims, err := verify(map[string]any{"amr": []any{"pwd", 5}}); err != nil || claims.AMR != nil {
		t.Errorf("amr with a non-string is NULL: %+v %v", claims, err)
	}

	rogue := newTestKey(t, "k1")
	if _, err := verifyIDToken(t.Context(), verifier, rogue.sign(t, idClaims(nil)), "web", "n1", time.Now()); err == nil {
		t.Error("a token signed by another key: accepted")
	}
}

func TestSignInKeysRefetchAtOnceAndAnUnservableSetIsUnavailable(t *testing.T) {
	old := newTestKey(t, "k1")
	idp := newFakeProvider(t, old)
	keys := &issuerKeys{issuer: idp.server.URL, client: &http.Client{}, jwksURI: idp.server.URL + "/jwks"}
	verifier := oidc.NewVerifier(idp.server.URL, keySet{keys}, &oidc.Config{ClientID: "web",
		SupportedSigningAlgs: idTokenAlgorithms, SkipExpiryCheck: true})
	token := func(k testKey) string {
		return k.sign(t, idClaims(map[string]any{"iss": idp.server.URL}))
	}
	if _, err := verifyIDToken(t.Context(), verifier, token(old), "web", "n1", time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		rotated := newTestKey(t, fmt.Sprintf("r%d", i))
		idp.setKeys(rotated)
		if _, err := verifyIDToken(t.Context(), verifier, token(rotated), "web", "n1", time.Now()); err != nil {
			t.Errorf("rotation %d: a sign-in right after a rotation is refused: %v", i, err)
		}
	}

	idp.setKeys(newTestKey(t, "r9"))
	idp.down.Store(true)
	if err := keys.prefetch(t.Context(), token(newTestKey(t, "r9"))); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("keys the provider cannot serve: %v", err)
	}
	if err := keys.prefetch(t.Context(), "not-a-jwt"); err != nil {
		t.Errorf("a bad token is the verifier's to refuse: %v", err)
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/app?tab=2": "/app?tab=2", "": "/", "app": "/", "//evil.example/x": "/", `/\evil.example`: "/",
		"https://evil.example/": "/", "/\r\nSet-Cookie: x=1": "/", "/\t/x": "/", "/\x00x": "/", "/caf€": "/", "/a b": "/",
	} {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPendingLoginsAreUsableOnce(t *testing.T) {
	p := newPendingLogins(time.Minute, 2)
	key := p.put(pendingLogin{state: "s1"})
	if got, ok := p.take(key); !ok || got.state != "s1" {
		t.Fatalf("take: %+v %v", got, ok)
	}
	if _, ok := p.take(key); ok {
		t.Error("a second take must fail")
	}
	if _, ok := p.take(""); ok {
		t.Error("no cookie, no sign-in")
	}

	first := p.put(pendingLogin{state: "a"})
	time.Sleep(time.Millisecond)
	p.put(pendingLogin{state: "b"})
	p.put(pendingLogin{state: "c"})
	if _, ok := p.take(first); ok {
		t.Error("over the limit the oldest goes first")
	}

	expired := newPendingLogins(time.Millisecond, 10)
	key = expired.put(pendingLogin{})
	time.Sleep(5 * time.Millisecond)
	if _, ok := expired.take(key); ok {
		t.Error("an expired sign-in must fail")
	}
}

func TestOIDCConfiguration(t *testing.T) {
	for issuer, ok := range map[string]bool{"https://idp.example": true, "http://127.0.0.1:9000": true,
		"http://localhost:9000": true, "http://idp.example": false, "ftp://idp.example": false} {
		_, err := NewOIDCLogin(OIDCConfig{Issuer: issuer, ClientID: "c", RedirectURI: "https://app/auth/callback"}, nil)
		if (err == nil) != ok {
			t.Errorf("issuer %s: %v", issuer, err)
		}
	}
	if _, err := NewOIDCLogin(OIDCConfig{Issuer: "https://idp.example"}, nil); err == nil {
		t.Error("a client id and a redirect URI are required")
	}
}
