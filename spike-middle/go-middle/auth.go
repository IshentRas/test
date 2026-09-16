package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const ctxEmail contextKey = "email"

type Service struct {
	Config Config
	Coder  *CoderClient
	Store  *TokenStore

	jwksMu sync.Mutex
	jwks   keyfunc.Keyfunc
}

func (s *Service) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email, err := s.emailFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxEmail, email)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) emailFromRequest(r *http.Request) (string, error) {
	email, _, err := s.resolveAppAuth(r)
	return email, err
}

// resolveAppAuth returns email + Coder user token from cookie, ?mt=, or Bearer IdP JWT.
func (s *Service) resolveAppAuth(r *http.Request) (email, coderToken string, err error) {
	if c, e := r.Cookie("middle_session"); e == nil && c.Value != "" {
		if sess, ok := s.Store.GetSession(c.Value); ok {
			return sess.Email, sess.CoderToken, nil
		}
	}
	if mt := r.URL.Query().Get("mt"); mt != "" {
		if t, ok := s.Store.GetMiddleToken(mt); ok {
			return t.Email, t.CoderToken, nil
		}
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		email, e := s.parseIDPJWT(r.Context(), strings.TrimSpace(h[7:]))
		if e != nil {
			return "", "", e
		}
		tok, e := s.ensureCoderToken(r.Context(), email)
		if e != nil {
			return "", "", e
		}
		return email, tok, nil
	}
	return "", "", fmt.Errorf("missing or invalid auth")
}

func (s *Service) coderTokenFromAppRequest(r *http.Request) (email, coderToken string, errStatus int, errMsg string) {
	email, tok, err := s.resolveAppAuth(r)
	if err != nil {
		return "", "", http.StatusUnauthorized, "app auth required (cookie, ?mt=, or Bearer JWT)"
	}
	return email, tok, 0, ""
}

func (s *Service) getJWKS(ctx context.Context) (keyfunc.Keyfunc, error) {
	s.jwksMu.Lock()
	defer s.jwksMu.Unlock()
	if s.jwks != nil {
		return s.jwks, nil
	}
	kf, err := keyfunc.NewDefaultCtx(ctx, []string{s.Config.OIDCJWKSURL})
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	s.jwks = kf
	log.Printf("loaded IdP JWKS from %s", s.Config.OIDCJWKSURL)
	return s.jwks, nil
}

func (s *Service) parseIDPJWT(ctx context.Context, raw string) (string, error) {
	kf, err := s.getJWKS(ctx)
	if err != nil {
		return "", err
	}
	tok, err := jwt.Parse(raw, kf.Keyfunc,
		jwt.WithIssuer(s.Config.OIDCIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithValidMethods([]string{
			jwt.SigningMethodRS256.Alg(),
			jwt.SigningMethodRS384.Alg(),
			jwt.SigningMethodRS512.Alg(),
			jwt.SigningMethodES256.Alg(),
			jwt.SigningMethodES384.Alg(),
			jwt.SigningMethodES512.Alg(),
		}),
	)
	if err != nil {
		return "", fmt.Errorf("invalid jwt: %w", err)
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		return "", fmt.Errorf("invalid jwt claims")
	}
	if s.Config.OIDCAudience != "" {
		found := false
		switch aud := claims["aud"].(type) {
		case string:
			found = aud == s.Config.OIDCAudience
		case []any:
			for _, a := range aud {
				as, ok := a.(string)
				if ok && as == s.Config.OIDCAudience {
					found = true
					break
				}
			}
		}
		if !found {
			return "", fmt.Errorf("jwt audience mismatch")
		}
	}
	claim := s.Config.EmailClaim
	if claim == "" {
		claim = "email"
	}
	email, _ := claims[claim].(string)
	if email == "" {
		return "", fmt.Errorf("jwt missing %s claim", claim)
	}
	return email, nil
}

func emailFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxEmail).(string)
	return v
}

func (s *Service) ensureCoderToken(ctx context.Context, email string) (string, error) {
	if t, ok := s.Store.GetCoderTokenByEmail(email); ok {
		if err := s.Coder.PingToken(ctx, t); err == nil {
			return t, nil
		}
	}
	user, err := s.Coder.FindOrCreateUser(ctx, email)
	if err != nil {
		return "", err
	}
	token, err := s.Coder.CreateUserToken(ctx, user.ID, "middle")
	if err != nil {
		return "", err
	}
	s.Store.SetCoderTokenByEmail(email, token)
	return token, nil
}

func (s *Service) setSessionCookie(w http.ResponseWriter, sessionID string) {
	c := &http.Cookie{
		Name:     "middle_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	}
	if strings.HasPrefix(strings.ToLower(s.Config.PublicURL), "https://") {
		c.Secure = true
	}
	http.SetCookie(w, c)
}

func (s *Service) checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if s.Config.allowedOrigin(origin) {
		return true
	}
	return strings.HasPrefix(origin, s.Config.PublicURL)
}
