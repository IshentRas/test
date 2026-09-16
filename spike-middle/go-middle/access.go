package main

import (
	"fmt"
	"net/http"
	"net/url"
)

type workspaceAccess struct {
	WorkspaceID   string         `json:"workspace_id"`
	WorkspaceName string         `json:"workspace_name"`
	Username      string         `json:"username"`
	Started       bool           `json:"started"`
	StartupReady  bool           `json:"startup_ready"`
	AgentID       string         `json:"agent_id,omitempty"`
	AgentName     string         `json:"agent_name,omitempty"`
	AgentDir      string         `json:"agent_directory,omitempty"`
	DisplayApps   []string       `json:"display_apps"`
	Choices       []accessChoice `json:"choices"`
}

type accessChoice struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // terminal | vscode_browser | vscode_desktop | app | ssh
	Label       string `json:"label"`
	Slug        string `json:"slug,omitempty"`
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable_reason,omitempty"`
}

func (s *Service) handleWorkspaceAccess(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := r.PathValue("id")
	raw, err := s.Coder.WorkspaceRaw(r.Context(), tok, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	me, err := s.Coder.GetMe(r.Context(), tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, buildWorkspaceAccess(raw, me.Username))
}

func (s *Service) handleVSCodeDesktop(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := r.PathValue("id")
	raw, err := s.Coder.WorkspaceRaw(r.Context(), tok, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	me, err := s.Coder.GetMe(r.Context(), tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	access := buildWorkspaceAccess(raw, me.Username)
	if !access.StartupReady {
		http.Error(w, "workspace agent not ready", http.StatusConflict)
		return
	}
	hasDesktop := false
	for _, d := range access.DisplayApps {
		if d == "vscode" || d == "vscode_insiders" {
			hasDesktop = true
			break
		}
	}
	if !hasDesktop {
		http.Error(w, "VS Code Desktop is not enabled for this agent (display_apps)", http.StatusNotFound)
		return
	}

	// Mint a middle token so the desktop extension talks to middle (/api/v2 + /derp),
	// not the real Coder URL — same illusion as `coder login` / `coder ssh`.
	mt := s.Store.IssueMiddleToken(email, tok)
	appScheme := "vscode"
	for _, d := range access.DisplayApps {
		if d == "vscode_insiders" {
			appScheme = "vscode-insiders"
			break
		}
	}
	q := url.Values{
		"owner":      {access.Username},
		"workspace":  {access.WorkspaceName},
		"url":        {s.Config.PublicURL},
		"token":      {mt.Token},
		"openRecent": {"true"},
	}
	if access.AgentName != "" {
		q.Set("agent", access.AgentName)
	}
	if access.AgentDir != "" {
		q.Set("folder", access.AgentDir)
	} else {
		q.Set("folder", "/home/coder")
	}
	uri := appScheme + "://coder.coder-remote/open?" + q.Encode()
	writeJSON(w, http.StatusOK, map[string]any{
		"uri":          uri,
		"middle_token": mt.Token,
		"coder_url":    s.Config.PublicURL,
	})
}

// handleOpenApp sets a session cookie then redirects into the path-based app proxy
// without putting mt= on the app URL (which previously corrupted code-server folder=).
func (s *Service) handleOpenApp(w http.ResponseWriter, r *http.Request) {
	email, coderTok, status, msg := s.coderTokenFromAppRequest(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	sess := s.Store.CreateSession(email, coderTok)
	s.setSessionCookie(w, sess.ID)

	id := r.PathValue("id")
	slug := r.PathValue("slug")
	if slug == "" {
		slug = "code-server"
	}
	raw, err := s.Coder.WorkspaceRaw(r.Context(), coderTok, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	me, err := s.Coder.GetMe(r.Context(), coderTok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	name, _ := raw["name"].(string)
	if name == "" {
		http.Error(w, "workspace name missing", http.StatusBadGateway)
		return
	}
	target := fmt.Sprintf("/proxy/%s/%s/apps/%s/?folder=%s",
		url.PathEscape(me.Username),
		url.PathEscape(name),
		url.PathEscape(slug),
		url.QueryEscape("/home/coder"),
	)
	http.Redirect(w, r, target, http.StatusFound)
}

func buildWorkspaceAccess(raw map[string]any, username string) workspaceAccess {
	name, _ := raw["name"].(string)
	id, _ := raw["id"].(string)
	agent := pickAgent(raw)
	started := workspaceStarted(raw)
	ready := started && agentReady(agent)

	var display []string
	var agentID, agentName, agentDir string
	if agent != nil {
		agentID, _ = agent["id"].(string)
		agentName, _ = agent["name"].(string)
		if d, ok := agent["expanded_directory"].(string); ok && d != "" {
			agentDir = d
		} else if d, ok := agent["directory"].(string); ok {
			agentDir = d
		}
		if items, ok := agent["display_apps"].([]any); ok {
			for _, it := range items {
				if s, ok := it.(string); ok {
					display = append(display, s)
				}
			}
		}
	}

	reason := ""
	if !started {
		reason = "Workspace is not started"
	} else if !ready {
		reason = "Agent startup still running"
	}

	choices := []accessChoice{}

	// Built-in web terminal (always offered when an agent exists — proxied by middle).
	choices = append(choices, accessChoice{
		ID:          "terminal",
		Kind:        "terminal",
		Label:       "Terminal",
		Available:   ready && agentID != "",
		Unavailable: pickUnavailable(ready && agentID != "", reason, "No agent"),
	})

	displaySet := map[string]bool{}
	for _, d := range display {
		displaySet[d] = true
	}

	if displaySet["vscode"] || displaySet["vscode_insiders"] {
		choices = append(choices, accessChoice{
			ID:          "vscode_desktop",
			Kind:        "vscode_desktop",
			Label:       "VS Code Desktop",
			Available:   ready,
			Unavailable: pickUnavailable(ready, reason, ""),
		})
	}

	// Template-defined apps (code-server, jupyter, …).
	if agent != nil {
		if apps, ok := agent["apps"].([]any); ok {
			for _, a := range apps {
				am, _ := a.(map[string]any)
				if am == nil {
					continue
				}
				slug, _ := am["slug"].(string)
				if slug == "" {
					continue
				}
				label, _ := am["display_name"].(string)
				if label == "" {
					label = slug
				}
				if slug == "code-server" {
					label = "VS Code Browser"
				}
				external, _ := am["external"].(bool)
				kind := "app"
				if slug == "code-server" {
					kind = "vscode_browser"
				}
				if external {
					kind = "external_app"
				}
				choices = append(choices, accessChoice{
					ID:          "app:" + slug,
					Kind:        kind,
					Label:       label,
					Slug:        slug,
					Available:   ready && !external,
					Unavailable: pickUnavailable(ready && !external, reason, "External app — open from Coder dashboard"),
				})
			}
		}
	}

	if displaySet["ssh_helper"] {
		choices = append(choices, accessChoice{
			ID:          "ssh",
			Kind:        "ssh",
			Label:       "SSH",
			Available:   ready,
			Unavailable: pickUnavailable(ready, reason, ""),
		})
	}

	return workspaceAccess{
		WorkspaceID:   id,
		WorkspaceName: name,
		Username:      username,
		Started:       started,
		StartupReady:  ready,
		AgentID:       agentID,
		AgentName:     agentName,
		AgentDir:      agentDir,
		DisplayApps:   display,
		Choices:       choices,
	}
}

func pickUnavailable(ok bool, reason, fallback string) string {
	if ok {
		return ""
	}
	if reason != "" {
		return reason
	}
	return fallback
}

func workspaceStarted(raw map[string]any) bool {
	lb, _ := raw["latest_build"].(map[string]any)
	if lb == nil {
		return false
	}
	job, _ := lb["job"].(map[string]any)
	status, _ := job["status"].(string)
	if status == "" {
		status, _ = lb["status"].(string)
	}
	transition, _ := lb["transition"].(string)
	return status == "succeeded" && transition == "start"
}

func pickAgent(raw map[string]any) map[string]any {
	lb, _ := raw["latest_build"].(map[string]any)
	if lb == nil {
		return nil
	}
	resources, _ := lb["resources"].([]any)
	var first map[string]any
	for _, res := range resources {
		rm, _ := res.(map[string]any)
		agents, _ := rm["agents"].([]any)
		for _, a := range agents {
			am, _ := a.(map[string]any)
			if am == nil {
				continue
			}
			if first == nil {
				first = am
			}
			if name, _ := am["name"].(string); name == "main" {
				return am
			}
		}
	}
	return first
}

func agentReady(agent map[string]any) bool {
	if agent == nil {
		return false
	}
	life, _ := agent["lifecycle_state"].(string)
	return life == "ready"
}
