package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func (s *Service) handleBuildInfo(w http.ResponseWriter, r *http.Request) {
	// Prefer proxying real buildinfo so CLI version checks pass; rewrite dashboard URL to middle.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.Config.CoderURL+"/api/v2/buildinfo", nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	res, err := s.Coder.HTTP.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	raw["dashboard_url"] = s.Config.PublicURL
	if _, ok := raw["external_url"]; ok {
		raw["external_url"] = s.Config.PublicURL
	}
	writeJSON(w, http.StatusOK, raw)
}

func (s *Service) handleAPIV2Proxy(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v2/buildinfo" {
		s.handleBuildInfo(w, r)
		return
	}

	target, err := url.Parse(s.Config.CoderURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Pre-login probes from `coder login` (no session token yet).
	anonymousOK := r.Method == http.MethodGet && (r.URL.Path == "/api/v2/users/first" ||
		r.URL.Path == "/api/v2/users/authmethods" ||
		strings.HasPrefix(r.URL.Path, "/api/v2/appearance"))

	incoming := r.Header.Get("Coder-Session-Token")
	var coderTok string
	if incoming != "" {
		var ok bool
		coderTok, ok = s.Store.ResolveCoderToken(incoming)
		if !ok {
			http.Error(w, "unknown middle token — use /cli-auth", http.StatusUnauthorized)
			return
		}
	} else if !anonymousOK {
		http.Error(w, "Coder-Session-Token required", http.StatusUnauthorized)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // stream websockets / SSE
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.Host = target.Host
		if coderTok != "" {
			req.Header.Set("Coder-Session-Token", coderTok)
		} else {
			req.Header.Del("Coder-Session-Token")
		}
		req.Header.Del("Origin")
		req.Header.Del("Referer")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Rewrite embedded DERP relay in agent connection info so clients hit middle /derp.
		if resp.StatusCode == http.StatusOK &&
			strings.Contains(r.URL.Path, "/workspaceagents/") &&
			strings.HasSuffix(r.URL.Path, "/connection") &&
			strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return s.rewriteConnectionInfoDERP(resp)
		}
		return nil
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, e error) {
		log.Printf("api proxy error %s: %v", r.URL.Path, e)
		http.Error(rw, "upstream error: "+e.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Service) rewriteConnectionInfoDERP(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	pub, err := url.Parse(s.Config.PublicURL)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	host := pub.Hostname()
	port := pub.Port()
	if port == "" {
		if pub.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNum := 0
	fmt.Sscanf(port, "%d", &portNum)

	derpMap, _ := raw["derp_map"].(map[string]any)
	regions, _ := derpMap["Regions"].(map[string]any)
	for _, regionAny := range regions {
		region, _ := regionAny.(map[string]any)
		if emb, _ := region["EmbeddedRelay"].(bool); !emb {
			continue
		}
		nodes, _ := region["Nodes"].([]any)
		for _, nAny := range nodes {
			n, _ := nAny.(map[string]any)
			if n == nil {
				continue
			}
			n["HostName"] = host
			n["DERPPort"] = float64(portNum)
			n["ForceHTTP"] = pub.Scheme == "http"
		}
	}
	out, err := json.Marshal(raw)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(out)))
	return nil
}

// handleDERP proxies Tailscale DERP (used by coder ssh agent dial) to upstream Coder.
func (s *Service) handleDERP(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(s.Config.CoderURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	incoming := r.Header.Get("Coder-Session-Token")
	coderTok := ""
	if incoming != "" {
		if t, ok := s.Store.ResolveCoderToken(incoming); ok {
			coderTok = t
		} else {
			// Some DERP probes may send the real Coder token after client rewrite quirks;
			// pass through unknown tokens and let Coder decide.
			coderTok = incoming
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.Host = target.Host
		if coderTok != "" {
			req.Header.Set("Coder-Session-Token", coderTok)
		}
		req.Header.Del("Origin")
		req.Header.Del("Referer")
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, e error) {
		log.Printf("derp proxy error: %v", e)
		http.Error(rw, "derp upstream error: "+e.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// handleBinProxy forwards /bin/* (CLI binaries the VS Code Desktop extension
// downloads from the configured Coder URL) to upstream Coder.
func (s *Service) handleBinProxy(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(s.Config.CoderURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.Host = target.Host
		req.Header.Del("Origin")
		req.Header.Del("Referer")
		// Optional: resolve middle tokens if a client sends one.
		if incoming := req.Header.Get("Coder-Session-Token"); incoming != "" {
			if t, ok := s.Store.ResolveCoderToken(incoming); ok {
				req.Header.Set("Coder-Session-Token", t)
			}
		}
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, e error) {
		log.Printf("bin proxy error %s: %v", r.URL.Path, e)
		http.Error(rw, "bin upstream error: "+e.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func (s *Service) handleWorkspaceAppProxy(w http.ResponseWriter, r *http.Request) {
	email, coderTok, status, msg := s.coderTokenFromAppRequest(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	// Always refresh session cookie so follow-up redirects work without ?mt=
	sess := s.Store.CreateSession(email, coderTok)
	s.setSessionCookie(w, sess.ID)

	user := r.PathValue("user")
	workspace := r.PathValue("workspace")
	slug := r.PathValue("slug")
	extra := r.PathValue("path")
	if user == "" || workspace == "" || slug == "" {
		http.Error(w, "bad proxy path", 400)
		return
	}

	target, err := url.Parse(s.Config.CoderURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	upstreamPath := "/@" + user + "/" + workspace + "/apps/" + slug
	if extra != "" {
		upstreamPath += "/" + extra
	}
	// Preserve trailing semantics for root
	if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(upstreamPath, "/") {
		upstreamPath += "/"
	}

	incomingQuery := r.URL.Query()
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = upstreamPath
		req.URL.RawPath = upstreamPath
		// Forward query except middle-only mt=. Sanitize a previously-corrupted
		// folder=/home/coder?mt=… value that code-server may still redirect to.
		q := url.Values{}
		for k, vs := range incomingQuery {
			if k == "mt" {
				continue
			}
			for _, v := range vs {
				if k == "folder" {
					v = sanitizeCodeServerFolder(v)
				}
				q.Add(k, v)
			}
		}
		// Force a clean workspace folder for code-server when absent/corrupted.
		if slug == "code-server" {
			if folder := q.Get("folder"); folder == "" || strings.Contains(folder, "mt=") {
				q.Set("folder", "/home/coder")
			}
		}
		req.URL.RawQuery = q.Encode()
		req.Host = target.Host
		req.Header.Set("Coder-Session-Token", coderTok)
		// Critical: Coder WS returns 403 if Origin is present from another host
		req.Header.Del("Origin")
		req.Header.Del("Referer")
		// Critical: browsers treat cookies as host-only (no port) for localhost, so an
		// owner session from :3000 is also sent to middle :8081. If we forward Cookie,
		// Coder prefers that session over Coder-Session-Token → owner-share 404 on
		// path apps. Always auth via the header we mint for the portal user.
		req.Header.Del("Cookie")
	}
	proxyPrefix := "/proxy/" + user + "/" + workspace + "/apps/" + slug
	coderPrefix := "/@" + user + "/" + workspace + "/apps/" + slug
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		resp.Header.Del("Content-Security-Policy")
		if loc := resp.Header.Get("Location"); loc != "" {
			loc = strings.Replace(loc, s.Config.CoderURL, s.Config.PublicURL, 1)
			loc = strings.Replace(loc, coderPrefix, proxyPrefix, 1)
			if slug == "code-server" {
				loc = sanitizeCodeServerLocation(loc)
			}
			// Never inject mt= into Location (corrupts folder=). Cookie carries auth.
			resp.Header.Set("Location", loc)
		}
		if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
			resp.Header.Del("Set-Cookie")
			for _, c := range cookies {
				c = strings.ReplaceAll(c, "Path="+coderPrefix, "Path="+proxyPrefix)
				c = strings.ReplaceAll(c, "Path="+coderPrefix+"/", "Path="+proxyPrefix+"/")
				resp.Header.Add("Set-Cookie", c)
			}
		}
		return nil
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, e error) {
		log.Printf("app proxy error slug=%s: %v", slug, e)
		http.Error(rw, "upstream error", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}

// sanitizeCodeServerFolder strips a leaked ?mt=… / &mt=… suffix that earlier
// portal builds accidentally baked into code-server's folder redirect.
func sanitizeCodeServerFolder(folder string) string {
	folder = strings.TrimSpace(folder)
	if i := strings.Index(folder, "?mt="); i >= 0 {
		folder = folder[:i]
	}
	if i := strings.Index(folder, "&mt="); i >= 0 {
		folder = folder[:i]
	}
	if i := strings.Index(folder, "%3Fmt%3D"); i >= 0 {
		folder = folder[:i]
	}
	if i := strings.Index(folder, "%3fmt%3d"); i >= 0 {
		folder = folder[:i]
	}
	if folder == "" {
		return "/home/coder"
	}
	return folder
}

func sanitizeCodeServerLocation(loc string) string {
	if !strings.Contains(loc, "folder=") {
		return loc
	}
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	q := u.Query()
	if folder := q.Get("folder"); folder != "" {
		clean := sanitizeCodeServerFolder(folder)
		if clean != folder {
			q.Set("folder", clean)
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return loc
}
