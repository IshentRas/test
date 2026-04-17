package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	defaultListenAddr = ":8080"
	authRetryDelay    = 10 * time.Second
	requestTimeout    = 10 * time.Second
	cacheTTL          = 1 * time.Hour
)

var (
	opsProcessed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "proxy_processed_requests_total",
		Help: "The total number of requests processed by the proxy",
	})
	cacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "proxy_cache_hits_total",
		Help: "The total number of successful in-memory cache lookups",
	})
	cacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "proxy_cache_misses_total",
		Help: "The total number of cache misses requiring a Vault API call",
	})
	vaultErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "proxy_vault_errors_total",
		Help: "The total number of failed Vault Transit decryption attempts",
	})
	tokenStatus = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "proxy_vault_token_valid",
		Help: "Binary status of the internal Vault token (1 for valid, 0 for missing)",
	})

	loadDefaultAWSConfig  = config.LoadDefaultConfig
	presignCallerIdentity = defaultPresignCallerIdentity
)

type presignedRequestData struct {
	url          string
	method       string
	signedHeader http.Header
}

func defaultPresignCallerIdentity(ctx context.Context, awsCfg aws.Config) (*presignedRequestData, error) {
	stsClient := sts.NewPresignClient(sts.NewFromConfig(awsCfg))
	presignedReq, err := stsClient.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}
	return &presignedRequestData{
		url:          presignedReq.URL,
		method:       presignedReq.Method,
		signedHeader: presignedReq.SignedHeader,
	}, nil
}

type appConfig struct {
	upstreamURL string
	vaultAddr   string
	vaultRole   string
	transitKey  string
	listenAddr  string
}

func loadConfig() (*appConfig, error) {
	cfg := &appConfig{
		upstreamURL: strings.TrimSpace(os.Getenv("UPSTREAM_URL")),
		vaultAddr:   strings.TrimSpace(os.Getenv("VAULT_ADDR")),
		vaultRole:   strings.TrimSpace(os.Getenv("VAULT_IAM_ROLE")),
		transitKey:  strings.TrimSpace(os.Getenv("TRANSIT_KEY_NAME")),
		listenAddr:  defaultListenAddr,
	}

	var missing []string
	if cfg.upstreamURL == "" {
		missing = append(missing, "UPSTREAM_URL")
	}
	if cfg.vaultAddr == "" {
		missing = append(missing, "VAULT_ADDR")
	}
	if cfg.vaultRole == "" {
		missing = append(missing, "VAULT_IAM_ROLE")
	}
	if cfg.transitKey == "" {
		missing = append(missing, "TRANSIT_KEY_NAME")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	if _, err := url.ParseRequestURI(cfg.vaultAddr); err != nil {
		return nil, fmt.Errorf("invalid VAULT_ADDR: %w", err)
	}
	if _, err := url.ParseRequestURI(cfg.upstreamURL); err != nil {
		return nil, fmt.Errorf("invalid UPSTREAM_URL: %w", err)
	}

	return cfg, nil
}

type SecureCache struct {
	sync.RWMutex
	items map[string]cacheItem
}

type cacheItem struct {
	value      string
	expiration int64
}

func newSecureCache() *SecureCache {
	return &SecureCache{items: make(map[string]cacheItem)}
}

func (c *SecureCache) Get(key string) (string, bool) {
	c.RLock()
	defer c.RUnlock()
	item, found := c.items[key]
	if !found || time.Now().UnixNano() > item.expiration {
		return "", false
	}
	return item.value, true
}

func (c *SecureCache) Set(key, value string, ttl time.Duration) {
	c.Lock()
	defer c.Unlock()
	c.items[key] = cacheItem{
		value:      value,
		expiration: time.Now().Add(ttl).UnixNano(),
	}
}

func (c *SecureCache) StartJanitor(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.deleteExpired()
		}
	}
}

func (c *SecureCache) deleteExpired() {
	now := time.Now().UnixNano()
	c.Lock()
	defer c.Unlock()
	for key, item := range c.items {
		if now > item.expiration {
			delete(c.items, key)
		}
	}
}

type vaultAuthManager struct {
	cfg        *appConfig
	httpClient *http.Client
	token      atomic.Value
	loginFn    func(context.Context) (string, int, error)
	waitFn     func(context.Context, time.Duration) bool
}

func newVaultAuthManager(cfg *appConfig, httpClient *http.Client) *vaultAuthManager {
	return &vaultAuthManager{
		cfg:        cfg,
		httpClient: httpClient,
		waitFn:     waitForDuration,
	}
}

func (m *vaultAuthManager) start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Println("[Auth] Initiating AWS IAM login to Vault...")
		token, leaseDuration, err := m.login(ctx)
		if err != nil {
			log.Printf("[Auth] Vault IAM login failed: %v", err)
			tokenStatus.Set(0)
			if !m.wait(ctx, authRetryDelay) {
				return
			}
			continue
		}

		m.token.Store(token)
		tokenStatus.Set(1)
		log.Println("[Vault] Authentication successful.")

		refreshIn := time.Duration(float64(leaseDuration)*0.8) * time.Second
		if refreshIn <= 0 {
			refreshIn = authRetryDelay
		}
		if !m.wait(ctx, refreshIn) {
			return
		}
	}
}

func (m *vaultAuthManager) login(ctx context.Context) (string, int, error) {
	if m.loginFn != nil {
		return m.loginFn(ctx)
	}
	return m.loginOnce(ctx)
}

func (m *vaultAuthManager) wait(ctx context.Context, d time.Duration) bool {
	if m.waitFn != nil {
		return m.waitFn(ctx, d)
	}
	return waitForDuration(ctx, d)
}

func waitForDuration(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (m *vaultAuthManager) loginOnce(ctx context.Context) (string, int, error) {
	awsCfg, err := loadDefaultAWSConfig(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("load aws config: %w", err)
	}

	presignedReq, err := presignCallerIdentity(ctx, awsCfg)
	if err != nil {
		return "", 0, fmt.Errorf("presign get-caller-identity: %w", err)
	}

	headers, err := json.Marshal(presignedReq.signedHeader)
	if err != nil {
		return "", 0, fmt.Errorf("marshal iam headers: %w", err)
	}
	loginData := map[string]string{
		"role":                    m.cfg.vaultRole,
		"iam_http_request_method": presignedReq.method,
		"iam_request_url":         base64.StdEncoding.EncodeToString([]byte(presignedReq.url)),
		"iam_request_body":        base64.StdEncoding.EncodeToString([]byte("")),
		"iam_request_headers":     base64.StdEncoding.EncodeToString(headers),
	}

	payload, err := json.Marshal(loginData)
	if err != nil {
		return "", 0, fmt.Errorf("marshal login payload: %w", err)
	}
	loginURL := fmt.Sprintf("%s/v1/auth/aws/login", m.cfg.vaultAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewBuffer(payload))
	if err != nil {
		return "", 0, fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("execute login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return "", 0, fmt.Errorf("vault login status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, fmt.Errorf("decode login response: %w", err)
	}
	if result.Auth.ClientToken == "" {
		return "", 0, errors.New("vault returned empty client token")
	}
	return result.Auth.ClientToken, result.Auth.LeaseDuration, nil
}

func (m *vaultAuthManager) tokenValue() (string, bool) {
	raw := m.token.Load()
	if raw == nil {
		return "", false
	}
	token, ok := raw.(string)
	return token, ok && token != ""
}

func decryptWithVault(ctx context.Context, cfg *appConfig, httpClient *http.Client, auth *vaultAuthManager, ciphertext string) (string, error) {
	token, ok := auth.tokenValue()
	if !ok {
		return "", fmt.Errorf("no vault token")
	}

	targetURL := fmt.Sprintf("%s/v1/transit/decrypt/%s", cfg.vaultAddr, cfg.transitKey)
	payload, err := json.Marshal(map[string]string{"ciphertext": ciphertext})
	if err != nil {
		return "", fmt.Errorf("marshal decrypt payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewBuffer(payload))
	if err != nil {
		return "", fmt.Errorf("build decrypt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", token)

	resp, err := httpClient.Do(req)
	if err != nil {
		vaultErrors.Inc()
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		vaultErrors.Inc()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return "", fmt.Errorf("vault decrypt status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		vaultErrors.Inc()
		return "", fmt.Errorf("decode decrypt response: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Data.Plaintext)
	if err != nil {
		vaultErrors.Inc()
		return "", fmt.Errorf("decode base64 plaintext: %w", err)
	}
	return string(decoded), nil
}

func prepareAuthorization(r *http.Request, cfg *appConfig, cache *SecureCache, auth *vaultAuthManager, httpClient *http.Client) error {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ENC(") || !strings.HasSuffix(authHeader, ")") {
		return nil
	}

	blob := strings.TrimSuffix(strings.TrimPrefix(authHeader, "Bearer ENC("), ")")
	if blob == "" {
		return errors.New("empty encrypted token blob")
	}

	rawKey, found := cache.Get(blob)
	if found {
		cacheHits.Inc()
	} else {
		cacheMisses.Inc()
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		var err error
		rawKey, err = decryptWithVault(ctx, cfg, httpClient, auth, blob)
		if err != nil {
			return err
		}
		cache.Set(blob, rawKey, cacheTTL)
	}

	r.Header.Set("Authorization", "Bearer "+rawKey)
	return nil
}

func newProxyHandler(cfg *appConfig, proxy *httputil.ReverseProxy, cache *SecureCache, authManager *vaultAuthManager, httpClient *http.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authManager.tokenValue(); ok {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("Not ready"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		opsProcessed.Inc()
		if err := prepareAuthorization(r, cfg, cache, authManager, httpClient); err != nil {
			log.Printf("[Security] Failed to process Authorization header: %v", err)
			http.Error(w, "failed to decrypt bearer token", http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	return mux
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	target, err := url.Parse(cfg.upstreamURL)
	if err != nil {
		log.Fatalf("invalid UPSTREAM_URL: %v", err)
	}

	httpClient := &http.Client{Timeout: requestTimeout}
	authManager := newVaultAuthManager(cfg, httpClient)
	cache := newSecureCache()

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go authManager.start(rootCtx)
	go cache.StartJanitor(rootCtx, 5*time.Minute)

	for {
		if _, ok := authManager.tokenValue(); ok {
			break
		}
		log.Println("Waiting for initial authentication...")
		select {
		case <-rootCtx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[Proxy] upstream error: %v", err)
		http.Error(w, "upstream proxy error", http.StatusBadGateway)
	}

	handler := newProxyHandler(cfg, proxy, cache, authManager, httpClient)

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-rootCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	log.Printf("[Proxy] Listening on %s (HTTP). Upstream: %s", cfg.listenAddr, cfg.upstreamURL)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
