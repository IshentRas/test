package main

import (
	"testing"
	"time"
)

func TestTokenStoreMiddleOpaque(t *testing.T) {
	s := NewTokenStore()
	mt := s.IssueMiddleToken("alice@example.com", "coder-secret-token")
	if mt.Token == "" || mt.Token == "coder-secret-token" {
		t.Fatalf("expected opaque middle token, got %q", mt.Token)
	}
	got, ok := s.ResolveCoderToken(mt.Token)
	if !ok || got != "coder-secret-token" {
		t.Fatalf("resolve failed: ok=%v got=%q", ok, got)
	}
	if _, ok := s.ResolveCoderToken("unknown"); ok {
		t.Fatal("unknown token should not resolve")
	}
}

func TestUsernameFromEmail(t *testing.T) {
	if got := usernameFromEmail("Alice.Demo@example.com"); got != "alice-demo" {
		t.Fatalf("got %q", got)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := Config{
		Listen:        ":8081",
		CoderURL:      "http://localhost:3000",
		OwnerToken:    "tok",
		OIDCIssuer:    "http://localhost:8080/realms/coder-lab",
		OIDCJWKSURL:   "http://localhost:8080/realms/coder-lab/protocol/openid-connect/certs",
		PublicURL:     "http://localhost:8081",
		PortalOrigins: []string{"http://localhost:5173"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.OwnerToken = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing owner token")
	}
	cfg.OwnerToken = "tok"
	cfg.OIDCIssuer = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing issuer")
	}
}

func TestPKCE(t *testing.T) {
	v, c, err := newPKCE()
	if err != nil || v == "" || c == "" {
		t.Fatalf("v=%q c=%q err=%v", v, c, err)
	}
}

func TestTokenStoreExpiry(t *testing.T) {
	s := NewTokenStore()
	mt := s.IssueMiddleToken("a@example.com", "coder-tok")
	if _, ok := s.GetMiddleToken(mt.Token); !ok {
		t.Fatal("fresh middle token should resolve")
	}
	s.mu.Lock()
	mt.ExpiresAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if _, ok := s.GetMiddleToken(mt.Token); ok {
		t.Fatal("expired middle token should not resolve")
	}
}
