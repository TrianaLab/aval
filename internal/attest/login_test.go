package attest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOIDC is a minimal OIDC provider: discovery, an authorize endpoint that
// logs the user in at once, a token endpoint that checks PKCE and returns an
// ES256 ID token, and its JWKS.
type fakeOIDC struct {
	*httptest.Server
	key       *ecdsa.PrivateKey
	mu        sync.Mutex
	nonce     string
	challenge string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: k}
	m := http.NewServeMux()
	f.Server = httptest.NewServer(m)
	t.Cleanup(f.Close)
	b64 := base64.RawURLEncoding.EncodeToString
	m.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"jwks_uri":                              f.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"ES256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	m.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "k",
			"x": b64(k.X.FillBytes(make([]byte, 32))), "y": b64(k.Y.FillBytes(make([]byte, 32))),
		}}})
	})
	m.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "sigstore" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nonce, f.challenge = q.Get("nonce"), q.Get("code_challenge")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {"c"}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		f.mu.Lock()
		nonce, challenge := f.nonce, f.challenge
		f.mu.Unlock()
		if r.PostForm.Get("code") != "c" || b64(sum[:]) != challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims, _ := json.Marshal(map[string]any{
			"iss": f.URL, "aud": "sigstore", "sub": "123", "nonce": nonce,
			"email": "dev@example.com", "email_verified": true,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
		})
		signing := b64([]byte(`{"alg":"ES256","kid":"k","typ":"JWT"}`)) + "." + b64(claims)
		h := sha256.Sum256([]byte(signing))
		rr, s, err := ecdsa.Sign(rand.Reader, k, h[:])
		if err != nil {
			t.Error(err)
		}
		sig := append(rr.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "expires_in": 60, "id_token": signing + "." + b64(sig)})
	})
	return f
}

func TestLoginInteractive(t *testing.T) {
	f := newFakeOIDC(t)
	var opened string
	open := func(u string) error {
		opened = u
		// The browser follows the provider's redirect to the local callback
		// while Login waits for it.
		go func() {
			if resp, err := http.Get(u); err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	var out strings.Builder
	tok, err := Login(f.URL, DefaultOIDCClientID, strings.NewReader(""), &out, open)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(opened, f.URL+"/authorize?") || !strings.Contains(out.String(), opened) {
		t.Errorf("login URL %q not opened and shown (output %q)", opened, out.String())
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(raw), `"email":"dev@example.com"`) {
		t.Errorf("token claims %s", raw)
	}
}

func TestLoginErrors(t *testing.T) {
	f := newFakeOIDC(t)
	// No browser: the code is read from in, and a wrong one is refused.
	noBrowser := func(string) error { return http.ErrNotSupported }
	var out strings.Builder
	if _, err := Login(f.URL, DefaultOIDCClientID, strings.NewReader("wrong\n"), &out, noBrowser); err == nil {
		t.Error("wrong verification code accepted")
	}
	if !strings.Contains(out.String(), "Enter verification code") {
		t.Errorf("no out-of-band prompt: %q", out.String())
	}
	if _, err := Login(f.URL+"/missing", DefaultOIDCClientID, strings.NewReader(""), &out, noBrowser); err == nil {
		t.Error("issuer without discovery accepted")
	}
}
