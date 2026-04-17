package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
)

func TestLoadConfigSuccess(t *testing.T) {
	t.Setenv("UPSTREAM_URL", "https://api.example.com")
	t.Setenv("VAULT_ADDR", "https://vault.example.com")
	t.Setenv("VAULT_IAM_ROLE", "proxy-role")
	t.Setenv("TRANSIT_KEY_NAME", "wallet-key")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned unexpected error: %v", err)
	}

	if cfg.upstreamURL != "https://api.example.com" {
		t.Fatalf("unexpected upstreamURL: %s", cfg.upstreamURL)
	}
	if cfg.vaultAddr != "https://vault.example.com" {
		t.Fatalf("unexpected vaultAddr: %s", cfg.vaultAddr)
	}
	if cfg.vaultRole != "proxy-role" {
		t.Fatalf("unexpected vaultRole: %s", cfg.vaultRole)
	}
	if cfg.transitKey != "wallet-key" {
		t.Fatalf("unexpected transitKey: %s", cfg.transitKey)
	}
}

func TestLoadConfigMissingEnv(t *testing.T) {
	t.Setenv("UPSTREAM_URL", "")
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_IAM_ROLE", "")
	t.Setenv("TRANSIT_KEY_NAME", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for missing required variables")
	}
}

func TestSecureCacheTTL(t *testing.T) {
	cache := newSecureCache()
	cache.Set("cipher", "plaintext", 20*time.Millisecond)

	if got, ok := cache.Get("cipher"); !ok || got != "plaintext" {
		t.Fatalf("expected cache hit with plaintext, got (%q, %t)", got, ok)
	}

	time.Sleep(35 * time.Millisecond)
	if _, ok := cache.Get("cipher"); ok {
		t.Fatal("expected cache miss after expiration")
	}
}

func TestPrepareAuthorizationDecryptsAndCaches(t *testing.T) {
	plaintext := "secret-token-123"
	plaintextB64 := base64.StdEncoding.EncodeToString([]byte(plaintext))
	ciphertext := "vault:v1:abc123"

	var decryptCalls int
	vaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/transit/decrypt/") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Vault-Token"); got != "vault-token" {
			t.Fatalf("unexpected vault token header: %s", got)
		}
		decryptCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"plaintext":"` + plaintextB64 + `"}}`))
	}))
	defer vaultServer.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vaultServer.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vaultServer.Client())
	auth.token.Store("vault-token")
	cache := newSecureCache()

	req := httptest.NewRequest(http.MethodGet, "http://proxy.local/resource", nil)
	req.Header.Set("Authorization", "Bearer ENC("+ciphertext+")")

	if err := prepareAuthorization(req, cfg, cache, auth, vaultServer.Client()); err != nil {
		t.Fatalf("prepareAuthorization returned error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+plaintext {
		t.Fatalf("expected rewritten auth header, got: %s", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://proxy.local/resource", nil)
	req2.Header.Set("Authorization", "Bearer ENC("+ciphertext+")")
	if err := prepareAuthorization(req2, cfg, cache, auth, vaultServer.Client()); err != nil {
		t.Fatalf("prepareAuthorization returned error on cached request: %v", err)
	}
	if got := req2.Header.Get("Authorization"); got != "Bearer "+plaintext {
		t.Fatalf("expected cached rewritten auth header, got: %s", got)
	}
	if decryptCalls != 1 {
		t.Fatalf("expected one vault decrypt call due to cache hit, got: %d", decryptCalls)
	}
}

func TestPrepareAuthorizationReturnsErrorForBadVaultResponse(t *testing.T) {
	vaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":["bad ciphertext"]}`))
	}))
	defer vaultServer.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vaultServer.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vaultServer.Client())
	auth.token.Store("vault-token")
	cache := newSecureCache()

	req := httptest.NewRequest(http.MethodGet, "http://proxy.local/resource", nil)
	req.Header.Set("Authorization", "Bearer ENC(vault:v1:bad)")

	err := prepareAuthorization(req, cfg, cache, auth, vaultServer.Client())
	if err == nil {
		t.Fatal("expected error for failed vault decrypt")
	}
}

func TestDecryptWithVaultNoToken(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	_, err := decryptWithVault(t.Context(), cfg, &http.Client{Timeout: time.Second}, auth, "vault:v1:any")
	if err == nil {
		t.Fatal("expected error when vault token is missing")
	}
}

func TestLoadConfigInvalidURL(t *testing.T) {
	_ = os.Setenv("UPSTREAM_URL", "://bad-url")
	_ = os.Setenv("VAULT_ADDR", "https://vault.example.com")
	_ = os.Setenv("VAULT_IAM_ROLE", "proxy-role")
	_ = os.Setenv("TRANSIT_KEY_NAME", "wallet-key")
	t.Cleanup(func() {
		_ = os.Unsetenv("UPSTREAM_URL")
		_ = os.Unsetenv("VAULT_ADDR")
		_ = os.Unsetenv("VAULT_IAM_ROLE")
		_ = os.Unsetenv("TRANSIT_KEY_NAME")
	})

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected URL validation error")
	}
}

func TestVaultResponsePayloadIsValidJSON(t *testing.T) {
	plaintext := "hello"
	plaintextB64 := base64.StdEncoding.EncodeToString([]byte(plaintext))
	body := []byte(`{"data":{"plaintext":"` + plaintextB64 + `"}}`)

	var payload struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("expected payload to unmarshal: %v", err)
	}
	if payload.Data.Plaintext != plaintextB64 {
		t.Fatalf("unexpected plaintext field value: %s", payload.Data.Plaintext)
	}
}

func TestProxyHandlerHealthAndReadiness(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	cache := newSecureCache()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("upstream"))
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	handler := newProxyHandler(cfg, proxy, cache, auth, upstream.Client())
	server := httptest.NewServer(handler)
	defer server.Close()

	healthResp, err := server.Client().Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("expected /healthz 200, got %d", healthResp.StatusCode)
	}

	readyRespBefore, err := server.Client().Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz before token failed: %v", err)
	}
	defer readyRespBefore.Body.Close()
	if readyRespBefore.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected /readyz 503 before token, got %d", readyRespBefore.StatusCode)
	}

	auth.token.Store("vault-token")

	readyRespAfter, err := server.Client().Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz after token failed: %v", err)
	}
	defer readyRespAfter.Body.Close()
	if readyRespAfter.StatusCode != http.StatusOK {
		t.Fatalf("expected /readyz 200 after token, got %d", readyRespAfter.StatusCode)
	}
}

func TestProxyHandlerDecryptsThenForwards(t *testing.T) {
	plaintext := "integration-secret-token"
	plaintextB64 := base64.StdEncoding.EncodeToString([]byte(plaintext))
	ciphertext := "vault:v1:int-test"

	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/transit/decrypt/wallet-key" {
			t.Fatalf("unexpected vault path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"plaintext":"` + plaintextB64 + `"}}`))
	}))
	defer vault.Close()

	var forwardedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	cfg := &appConfig{
		upstreamURL: upstream.URL,
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	httpClient := vault.Client()
	auth := newVaultAuthManager(cfg, httpClient)
	auth.token.Store("vault-token")
	cache := newSecureCache()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	handler := newProxyHandler(cfg, proxy, cache, auth, httpClient)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/messages", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer ENC("+ciphertext+")")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected proxy status 200, got %d (body: %s)", resp.StatusCode, string(body))
	}
	if forwardedAuth != "Bearer "+plaintext {
		t.Fatalf("expected forwarded auth to be rewritten, got: %s", forwardedAuth)
	}
}

func TestPrepareAuthorizationNoEncryptedHeaderNoChange(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	cache := newSecureCache()

	req := httptest.NewRequest(http.MethodGet, "http://proxy.local/resource", nil)
	req.Header.Set("Authorization", "Bearer plain-token")

	if err := prepareAuthorization(req, cfg, cache, auth, &http.Client{Timeout: time.Second}); err != nil {
		t.Fatalf("expected no error for non-encrypted header, got: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer plain-token" {
		t.Fatalf("header should remain unchanged, got: %s", got)
	}
}

func TestPrepareAuthorizationEmptyBlob(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	cache := newSecureCache()

	req := httptest.NewRequest(http.MethodGet, "http://proxy.local/resource", nil)
	req.Header.Set("Authorization", "Bearer ENC()")

	err := prepareAuthorization(req, cfg, cache, auth, &http.Client{Timeout: time.Second})
	if err == nil {
		t.Fatal("expected error for empty encrypted token blob")
	}
}

func TestDeleteExpiredRemovesOnlyExpiredItems(t *testing.T) {
	cache := newSecureCache()
	cache.items["expired"] = cacheItem{value: "x", expiration: time.Now().Add(-1 * time.Second).UnixNano()}
	cache.items["active"] = cacheItem{value: "y", expiration: time.Now().Add(10 * time.Second).UnixNano()}

	cache.deleteExpired()

	if _, ok := cache.items["expired"]; ok {
		t.Fatal("expected expired key to be removed")
	}
	if _, ok := cache.items["active"]; !ok {
		t.Fatal("expected active key to remain")
	}
}

func TestStartJanitorCleansExpiredEntries(t *testing.T) {
	cache := newSecureCache()
	cache.items["expired"] = cacheItem{value: "x", expiration: time.Now().Add(-1 * time.Second).UnixNano()}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		cache.StartJanitor(ctx, 5*time.Millisecond)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if _, ok := cache.items["expired"]; ok {
		t.Fatal("expected janitor to clean expired entry")
	}
}

func TestTokenValueStates(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})

	if _, ok := auth.tokenValue(); ok {
		t.Fatal("expected tokenValue to be false before token is set")
	}
	auth.token.Store("")
	if _, ok := auth.tokenValue(); ok {
		t.Fatal("expected tokenValue to be false for empty token")
	}
	auth.token.Store("vault-token")
	if token, ok := auth.tokenValue(); !ok || token != "vault-token" {
		t.Fatalf("expected valid token state, got token=%q ok=%t", token, ok)
	}
}

func TestDecryptWithVaultInvalidBase64(t *testing.T) {
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"plaintext":"%%%not-base64%%%"}}`))
	}))
	defer vault.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vault.Client())
	auth.token.Store("vault-token")

	_, err := decryptWithVault(t.Context(), cfg, vault.Client(), auth, "vault:v1:abc")
	if err == nil {
		t.Fatal("expected base64 decode error")
	}
}

func TestDecryptWithVaultInvalidJSON(t *testing.T) {
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":`))
	}))
	defer vault.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vault.Client())
	auth.token.Store("vault-token")

	_, err := decryptWithVault(t.Context(), cfg, vault.Client(), auth, "vault:v1:abc")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestProxyHandlerVaultFailureReturnsUnauthorizedAndDoesNotForward(t *testing.T) {
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":["bad ciphertext"]}`))
	}))
	defer vault.Close()

	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &appConfig{
		upstreamURL: upstream.URL,
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vault.Client())
	auth.token.Store("vault-token")
	cache := newSecureCache()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	handler := newProxyHandler(cfg, proxy, cache, auth, vault.Client())
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/messages", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer ENC(vault:v1:bad)")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on decrypt failure, got %d", resp.StatusCode)
	}
	if upstreamCalls != 0 {
		t.Fatalf("expected no upstream calls on decrypt failure, got %d", upstreamCalls)
	}
}

func TestProxyHandlerForwardsWithoutAuthorizationHeader(t *testing.T) {
	var forwardedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	cfg := &appConfig{
		upstreamURL: upstream.URL,
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	cache := newSecureCache()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("failed to parse upstream URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	handler := newProxyHandler(cfg, proxy, cache, auth, &http.Client{Timeout: time.Second})
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/anything")
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from upstream, got %d", resp.StatusCode)
	}
	if forwardedAuth != "" {
		t.Fatalf("expected empty forwarded Authorization header, got: %s", forwardedAuth)
	}
}

func TestWaitForDurationContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if ok := waitForDuration(ctx, time.Second); ok {
		t.Fatal("expected waitForDuration to return false when context is canceled")
	}
}

func TestWaitForDurationElapsed(t *testing.T) {
	if ok := waitForDuration(t.Context(), 1*time.Millisecond); !ok {
		t.Fatal("expected waitForDuration to return true when duration elapses")
	}
}

func TestVaultAuthManagerStartRetryThenSuccess(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})

	var calls int
	var waits int
	auth.loginFn = func(ctx context.Context) (string, int, error) {
		calls++
		if calls == 1 {
			return "", 0, errors.New("temporary failure")
		}
		return "vault-token", 10, nil
	}
	auth.waitFn = func(ctx context.Context, d time.Duration) bool {
		waits++
		return waits == 1
	}

	auth.start(t.Context())
	token, ok := auth.tokenValue()
	if !ok || token != "vault-token" {
		t.Fatalf("expected token to be set after successful retry, got token=%q ok=%t", token, ok)
	}
	if calls < 2 {
		t.Fatalf("expected at least two login attempts, got %d", calls)
	}
}

func TestVaultAuthManagerStartStopsOnRetryWaitCancel(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})

	auth.loginFn = func(ctx context.Context) (string, int, error) {
		return "", 0, errors.New("failure")
	}
	auth.waitFn = func(ctx context.Context, d time.Duration) bool {
		return false
	}

	auth.start(t.Context())
	if _, ok := auth.tokenValue(); ok {
		t.Fatal("expected no token to be set when login keeps failing")
	}
}

func TestVaultAuthManagerLoginDelegatesToLoginFn(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	auth.loginFn = func(ctx context.Context) (string, int, error) {
		return "token", 60, nil
	}
	token, lease, err := auth.login(t.Context())
	if err != nil {
		t.Fatalf("expected no error from custom loginFn, got %v", err)
	}
	if token != "token" || lease != 60 {
		t.Fatalf("unexpected login values: token=%s lease=%d", token, lease)
	}
}

func TestVaultAuthManagerWaitDelegatesToWaitFn(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	auth.waitFn = func(ctx context.Context, d time.Duration) bool {
		return d == 123*time.Millisecond
	}

	if !auth.wait(t.Context(), 123*time.Millisecond) {
		t.Fatal("expected wait wrapper to return true from custom waitFn")
	}
	if auth.wait(t.Context(), 99*time.Millisecond) {
		t.Fatal("expected wait wrapper to return false from custom waitFn")
	}
}

func TestVaultAuthManagerWaitFallsBackToDefault(t *testing.T) {
	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})
	auth.waitFn = nil

	if !auth.wait(t.Context(), 1*time.Millisecond) {
		t.Fatal("expected wait fallback to default implementation")
	}
}

func TestLoginOnceSuccessWithStubs(t *testing.T) {
	oldLoad := loadDefaultAWSConfig
	oldPresign := presignCallerIdentity
	t.Cleanup(func() {
		loadDefaultAWSConfig = oldLoad
		presignCallerIdentity = oldPresign
	})

	loadDefaultAWSConfig = func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, nil
	}
	presignCallerIdentity = func(ctx context.Context, awsCfg aws.Config) (*presignedRequestData, error) {
		return &presignedRequestData{
			url:          "https://sts.amazonaws.com",
			method:       http.MethodGet,
			signedHeader: http.Header{"X-Test": []string{"1"}},
		}, nil
	}

	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/aws/login" {
			t.Fatalf("unexpected login path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"auth":{"client_token":"token-123","lease_duration":120}}`))
	}))
	defer vault.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vault.Client())

	token, lease, err := auth.loginOnce(t.Context())
	if err != nil {
		t.Fatalf("expected loginOnce success, got %v", err)
	}
	if token != "token-123" || lease != 120 {
		t.Fatalf("unexpected login result token=%s lease=%d", token, lease)
	}
}

func TestVaultAuthManagerLoginFallsBackToLoginOnce(t *testing.T) {
	oldLoad := loadDefaultAWSConfig
	oldPresign := presignCallerIdentity
	t.Cleanup(func() {
		loadDefaultAWSConfig = oldLoad
		presignCallerIdentity = oldPresign
	})

	loadDefaultAWSConfig = func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, nil
	}
	presignCallerIdentity = func(ctx context.Context, awsCfg aws.Config) (*presignedRequestData, error) {
		return &presignedRequestData{
			url:          "https://sts.amazonaws.com",
			method:       http.MethodGet,
			signedHeader: http.Header{"X-Test": []string{"1"}},
		}, nil
	}

	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"auth":{"client_token":"from-login-once","lease_duration":10}}`))
	}))
	defer vault.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vault.Client())
	auth.loginFn = nil

	token, lease, err := auth.login(t.Context())
	if err != nil {
		t.Fatalf("expected fallback login to succeed, got %v", err)
	}
	if token != "from-login-once" || lease != 10 {
		t.Fatalf("unexpected fallback login result token=%s lease=%d", token, lease)
	}
}

func TestLoginOnceLoadConfigError(t *testing.T) {
	oldLoad := loadDefaultAWSConfig
	t.Cleanup(func() {
		loadDefaultAWSConfig = oldLoad
	})

	loadDefaultAWSConfig = func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, errors.New("boom")
	}

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})

	if _, _, err := auth.loginOnce(t.Context()); err == nil {
		t.Fatal("expected error when loading AWS config fails")
	}
}

func TestLoginOncePresignError(t *testing.T) {
	oldLoad := loadDefaultAWSConfig
	oldPresign := presignCallerIdentity
	t.Cleanup(func() {
		loadDefaultAWSConfig = oldLoad
		presignCallerIdentity = oldPresign
	})

	loadDefaultAWSConfig = func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, nil
	}
	presignCallerIdentity = func(ctx context.Context, awsCfg aws.Config) (*presignedRequestData, error) {
		return nil, errors.New("presign failed")
	}

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   "https://vault.example.com",
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, &http.Client{Timeout: time.Second})

	if _, _, err := auth.loginOnce(t.Context()); err == nil {
		t.Fatal("expected error when presign fails")
	}
}

func TestLoginOnceEmptyTokenError(t *testing.T) {
	oldLoad := loadDefaultAWSConfig
	oldPresign := presignCallerIdentity
	t.Cleanup(func() {
		loadDefaultAWSConfig = oldLoad
		presignCallerIdentity = oldPresign
	})

	loadDefaultAWSConfig = func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		return aws.Config{}, nil
	}
	presignCallerIdentity = func(ctx context.Context, awsCfg aws.Config) (*presignedRequestData, error) {
		return &presignedRequestData{
			url:          "https://sts.amazonaws.com",
			method:       http.MethodGet,
			signedHeader: http.Header{},
		}, nil
	}

	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"auth":{"client_token":"","lease_duration":30}}`))
	}))
	defer vault.Close()

	cfg := &appConfig{
		upstreamURL: "https://api.example.com",
		vaultAddr:   vault.URL,
		vaultRole:   "role",
		transitKey:  "wallet-key",
		listenAddr:  ":8080",
	}
	auth := newVaultAuthManager(cfg, vault.Client())

	if _, _, err := auth.loginOnce(t.Context()); err == nil {
		t.Fatal("expected error when Vault returns empty client token")
	}
}
