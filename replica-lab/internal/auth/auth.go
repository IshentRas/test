package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// PushCredentials holds the shared secret GitLab uses for push mirroring.
type PushCredentials struct {
	User     string
	Password string
}

// LoadPushCredentials reads PUSH_AUTH_USER and PUSH_AUTH_PASSWORD (or
// PUSH_AUTH_PASSWORD_FILE for a mounted Secret). Returns enabled=false when
// credentials are not configured (kind lab / open push).
func LoadPushCredentials() (PushCredentials, bool) {
	user := strings.TrimSpace(os.Getenv("PUSH_AUTH_USER"))
	pass := os.Getenv("PUSH_AUTH_PASSWORD")
	if path := os.Getenv("PUSH_AUTH_PASSWORD_FILE"); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			pass = strings.TrimRight(string(b), "\r\n")
		}
	}
	if user == "" || pass == "" {
		return PushCredentials{}, false
	}
	return PushCredentials{User: user, Password: pass}, true
}

// PushAuthMiddleware requires HTTP Basic Auth on git-receive-pack (push) only.
// upload-pack (fetch) stays open for in-cluster reconcilers.
func PushAuthMiddleware(creds PushCredentials, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isReceivePack(r) {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || !checkBasicAuth(user, pass, creds) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git-replica"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isReceivePack(r *http.Request) bool {
	if strings.Contains(r.URL.Path, "git-receive-pack") {
		return true
	}
	return r.URL.Query().Get("service") == "git-receive-pack"
}

func checkBasicAuth(user, pass string, creds PushCredentials) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(creds.User)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(creds.Password)) == 1
	return userOK && passOK
}
