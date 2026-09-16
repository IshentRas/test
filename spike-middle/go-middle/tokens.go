package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	sessionTTL     = 12 * time.Hour
	middleTokenTTL = 12 * time.Hour
)

type MiddleToken struct {
	Token      string
	Email      string
	CoderToken string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type Session struct {
	ID         string
	Email      string
	CoderToken string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type TokenStore struct {
	mu           sync.RWMutex
	byMiddle     map[string]*MiddleToken
	coderByEmail map[string]string
	sessions     map[string]*Session
}

func NewTokenStore() *TokenStore {
	return &TokenStore{
		byMiddle:     make(map[string]*MiddleToken),
		coderByEmail: make(map[string]string),
		sessions:     make(map[string]*Session),
	}
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func mustRandomHex(nBytes int) string {
	s, err := randomHex(nBytes)
	if err != nil {
		// Extremely unlikely; fall back so callers stay simple.
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return s
}

func (s *TokenStore) pruneLocked(now time.Time) {
	for k, t := range s.byMiddle {
		if now.After(t.ExpiresAt) {
			delete(s.byMiddle, k)
		}
	}
	for k, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, k)
		}
	}
}

func (s *TokenStore) IssueMiddleToken(email, coderToken string) *MiddleToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	now := time.Now()
	t := &MiddleToken{
		Token:      "mt_" + mustRandomHex(24),
		Email:      email,
		CoderToken: coderToken,
		CreatedAt:  now,
		ExpiresAt:  now.Add(middleTokenTTL),
	}
	s.byMiddle[t.Token] = t
	s.coderByEmail[email] = coderToken
	return t
}

func (s *TokenStore) GetMiddleToken(token string) (*MiddleToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	t, ok := s.byMiddle[token]
	if !ok || time.Now().After(t.ExpiresAt) {
		delete(s.byMiddle, token)
		return nil, false
	}
	return t, true
}

func (s *TokenStore) ResolveCoderToken(middleOrCoder string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	if t, ok := s.byMiddle[middleOrCoder]; ok {
		return t.CoderToken, true
	}
	// Passthrough of raw Coder tokens we minted (needed for some CLI/DERP quirks).
	for _, ct := range s.coderByEmail {
		if ct == middleOrCoder {
			return middleOrCoder, true
		}
	}
	return "", false
}

func (s *TokenStore) GetCoderTokenByEmail(email string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.coderByEmail[email]
	return t, ok
}

func (s *TokenStore) SetCoderTokenByEmail(email, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coderByEmail[email] = token
}

func (s *TokenStore) CreateSession(email, coderToken string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	now := time.Now()
	sess := &Session{
		ID:         "sess_" + mustRandomHex(16),
		Email:      email,
		CoderToken: coderToken,
		CreatedAt:  now,
		ExpiresAt:  now.Add(sessionTTL),
	}
	s.sessions[sess.ID] = sess
	s.coderByEmail[email] = coderToken
	return sess
}

func (s *TokenStore) GetSession(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	sess, ok := s.sessions[id]
	if !ok || time.Now().After(sess.ExpiresAt) {
		delete(s.sessions, id)
		return nil, false
	}
	return sess, true
}
