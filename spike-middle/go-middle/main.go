package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	store := NewTokenStore()
	coder := NewCoderClient(cfg.CoderURL, cfg.OwnerToken)
	svc := &Service{
		Config: cfg,
		Coder:  coder,
		Store:  store,
	}

	// Warm JWKS early so first request fails fast if IdP is down.
	if _, err := svc.getJWKS(context.Background()); err != nil {
		log.Printf("warning: IdP JWKS not ready yet (%v) — will retry on first request", err)
	}

	mux := http.NewServeMux()

	mux.Handle("GET /v1/me", svc.requireAuth(http.HandlerFunc(svc.handleMe)))
	mux.Handle("GET /v1/workspaces", svc.requireAuth(http.HandlerFunc(svc.handleListWorkspaces)))
	mux.Handle("POST /v1/workspaces", svc.requireAuth(http.HandlerFunc(svc.handleCreateWorkspace)))
	mux.Handle("DELETE /v1/workspaces/{id}", svc.requireAuth(http.HandlerFunc(svc.handleDeleteWorkspace)))
	mux.Handle("GET /v1/workspaces/{id}/access", svc.requireAuth(http.HandlerFunc(svc.handleWorkspaceAccess)))
	mux.Handle("POST /v1/workspaces/{id}/vscode-desktop", svc.requireAuth(http.HandlerFunc(svc.handleVSCodeDesktop)))
	mux.Handle("GET /v1/templates", svc.requireAuth(http.HandlerFunc(svc.handleListTemplates)))
	mux.Handle("POST /v1/app-session", svc.requireAuth(http.HandlerFunc(svc.handleAppSession)))

	mux.HandleFunc("GET /v1/workspaces/{id}/terminal", svc.handleTerminalPage)
	mux.HandleFunc("GET /v1/workspaces/{id}/pty", svc.handlePTY)
	mux.HandleFunc("GET /v1/workspaces/{id}/build-logs", svc.handleBuildLogs)
	mux.HandleFunc("GET /v1/workspaces/{id}/agent-logs", svc.handleAgentLogs)
	mux.HandleFunc("GET /v1/workspaces/{id}/open/{slug}", svc.handleOpenApp)
	mux.HandleFunc("/proxy/{user}/{workspace}/apps/{slug}/{path...}", svc.handleWorkspaceAppProxy)
	mux.HandleFunc("/proxy/{user}/{workspace}/apps/{slug}", svc.handleWorkspaceAppProxy)

	mux.HandleFunc("GET /cli-auth", svc.handleCliAuth)
	mux.HandleFunc("GET /cli-auth/start", svc.handleCliAuthStart)
	mux.HandleFunc("GET /cli-auth/callback", svc.handleCliAuthCallback)
	mux.HandleFunc("POST /cli-auth/mint", svc.handleCliAuthMint)
	mux.HandleFunc("GET /api/v2/buildinfo", svc.handleBuildInfo)
	mux.HandleFunc("/api/v2/", svc.handleAPIV2Proxy)

	mux.HandleFunc("/derp", svc.handleDERP)
	mux.HandleFunc("/derp/", svc.handleDERP)
	mux.HandleFunc("/bin/", svc.handleBinProxy)
	mux.HandleFunc("/bin", svc.handleBinProxy)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /v1/config/public", svc.handlePublicConfig)

	addr := cfg.Listen
	log.Printf("middle listening on %s (coder=%s idp=%s) pid=%d", addr, cfg.CoderURL, cfg.OIDCIssuer, os.Getpid())
	srv := &http.Server{
		Addr:              addr,
		Handler:           withCORS(cfg, withRecover(mux)),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       0,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		log.Printf("middle received %v — shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("middle stopped cleanly")
}

func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic on %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func withCORS(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if cfg.allowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Coder-Session-Token")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"public_url":  s.Config.PublicURL,
		"coder_hint":  "hidden — use middle only",
		"idp_issuer":  s.Config.OIDCIssuer,
		"email_claim": s.Config.EmailClaim,
	})
}
