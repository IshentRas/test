package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Short-lived PKCE verifiers for CLI auth browser flow.
var cliPKCE = struct {
	mu   sync.Mutex
	byState map[string]cliPKCEEntry
}{byState: map[string]cliPKCEEntry{}}

type cliPKCEEntry struct {
	Verifier  string
	ExpiresAt time.Time
}

func putCLIPKCE(state, verifier string) {
	cliPKCE.mu.Lock()
	defer cliPKCE.mu.Unlock()
	now := time.Now()
	for k, v := range cliPKCE.byState {
		if now.After(v.ExpiresAt) {
			delete(cliPKCE.byState, k)
		}
	}
	cliPKCE.byState[state] = cliPKCEEntry{Verifier: verifier, ExpiresAt: now.Add(10 * time.Minute)}
}

func takeCLIPKCE(state string) (string, bool) {
	cliPKCE.mu.Lock()
	defer cliPKCE.mu.Unlock()
	e, ok := cliPKCE.byState[state]
	if !ok || time.Now().After(e.ExpiresAt) {
		delete(cliPKCE.byState, state)
		return "", false
	}
	delete(cliPKCE.byState, state)
	return e.Verifier, true
}

func (s *Service) handleCliAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"/><title>CLI Auth — Middle</title>
<style>
  body{font-family:system-ui,sans-serif;max-width:560px;margin:48px auto;padding:0 16px;background:#0f172a;color:#e2e8f0}
  h1{font-size:1.25rem} .card{background:#1e293b;border-radius:8px;padding:20px;margin-top:16px}
  input,button{font:inherit;padding:8px 10px;border-radius:6px;border:1px solid #334155;background:#0f172a;color:#e2e8f0}
  button{cursor:pointer;background:#2563eb;border-color:#2563eb}
  code{background:#0f172a;padding:2px 6px;border-radius:4px;word-break:break-all}
  .token{display:block;margin-top:12px;padding:12px;background:#020617;border-radius:6px}
  .muted{color:#94a3b8;font-size:0.9rem}
  a.btn{display:inline-block;padding:10px 14px;background:#2563eb;color:#fff;border-radius:8px;text-decoration:none;font-weight:500}
</style></head><body>
  <h1>Session token</h1>
  <p class="muted">Middle facade for <code>coder login</code>. Use URL <code>%s</code>.</p>
  <div class="card">
    <p>Sign in with Keycloak (OIDC):</p>
    <p><a class="btn" href="/cli-auth/start">Login with Keycloak</a></p>
    <p class="muted">Or paste a Keycloak access token:</p>
    <input id="jwt" style="width:100%%" placeholder="Keycloak access_token"/>
    <p><button type="button" id="mint">Mint CLI token</button></p>
  </div>
  <div class="card" id="result" style="display:none">
    <p>Your session token:</p>
    <code class="token" id="tok"></code>
    <p><button type="button" id="copy">Copy</button></p>
    <p class="muted">Paste when the CLI prompts. Then <code>coder whoami</code> / <code>coder ls</code>.</p>
  </div>
<script>
async function mint(jwt) {
  const res = await fetch('/cli-auth/mint', {
    method: 'POST',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify({jwt})
  });
  const text = await res.text();
  if (!res.ok) { alert(text); return; }
  const data = JSON.parse(text);
  document.getElementById('result').style.display = 'block';
  document.getElementById('tok').textContent = data.middle_token;
}
document.getElementById('mint').onclick = () => {
  let v = document.getElementById('jwt').value.trim();
  if (v.toLowerCase().startsWith('bearer ')) v = v.slice(7);
  mint(v);
};
document.getElementById('copy').onclick = async () => {
  await navigator.clipboard.writeText(document.getElementById('tok').textContent);
};
const params = new URLSearchParams(location.search);
const hashParams = new URLSearchParams(location.hash.replace(/^#/, ''));
const tok = hashParams.get('middle_token') || params.get('middle_token');
if (tok) {
  document.getElementById('result').style.display = 'block';
  document.getElementById('tok').textContent = tok;
  history.replaceState({}, '', '/cli-auth');
}
</script>
</body></html>`, s.Config.PublicURL)
}

func (s *Service) handleCliAuthStart(w http.ResponseWriter, r *http.Request) {
	verifier, challenge, err := newPKCE()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	state, err := randomURLString(24)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	putCLIPKCE(state, verifier)
	q := url.Values{
		"client_id":             {"middle-service"},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"redirect_uri":          {s.Config.PublicURL + "/cli-auth/callback"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authURL := s.Config.OIDCIssuer + "/protocol/openid-connect/auth?" + q.Encode()
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Service) handleCliAuthCallback(w http.ResponseWriter, r *http.Request) {
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		http.Error(w, errMsg+": "+r.URL.Query().Get("error_description"), 400)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	verifier, ok := takeCLIPKCE(state)
	if !ok || code == "" {
		http.Error(w, "invalid or expired OIDC state", 400)
		return
	}
	tokenURL := s.Config.OIDCIssuer + "/protocol/openid-connect/token"
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"middle-service"},
		"code":          {code},
		"redirect_uri":  {s.Config.PublicURL + "/cli-auth/callback"},
		"code_verifier": {verifier},
	}
	res, err := http.PostForm(tokenURL, form)
	if err != nil {
		http.Error(w, "token exchange: "+err.Error(), 502)
		return
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		http.Error(w, "token exchange failed: "+string(body), res.StatusCode)
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		http.Error(w, "bad token response", 502)
		return
	}
	email, err := s.parseIDPJWT(r.Context(), tok.AccessToken)
	if err != nil {
		http.Error(w, err.Error(), 401)
		return
	}
	coderTok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	mt := s.Store.IssueMiddleToken(email, coderTok)
	sess := s.Store.CreateSession(email, coderTok)
	s.setSessionCookie(w, sess.ID)
	http.Redirect(w, r, "/cli-auth#middle_token="+url.QueryEscape(mt.Token), http.StatusFound)
}

func (s *Service) handleCliAuthMint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JWT string `json:"jwt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	if body.JWT == "" {
		http.Error(w, "jwt required (Keycloak access_token)", 400)
		return
	}
	raw := strings.TrimSpace(body.JWT)
	if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	email, err := s.parseIDPJWT(r.Context(), raw)
	if err != nil {
		http.Error(w, err.Error(), 401)
		return
	}
	coderTok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	mt := s.Store.IssueMiddleToken(email, coderTok)
	sess := s.Store.CreateSession(email, coderTok)
	s.setSessionCookie(w, sess.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"email":        email,
		"middle_token": mt.Token,
	})
}

func newPKCE() (verifier, challenge string, err error) {
	verifier, err = randomURLString(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomURLString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
