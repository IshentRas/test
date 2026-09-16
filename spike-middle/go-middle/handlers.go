package main

import (
	"encoding/json"
	"net/http"
	"regexp"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	me, err := s.Coder.GetMe(r.Context(), tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":         email,
		"coder_user":    me,
		"coder_url_hint": "hidden — use middle only",
	})
}

func (s *Service) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	me, err := s.Coder.GetMe(r.Context(), tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	list, err := s.Coder.ListWorkspaces(r.Context(), tok, me.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": list})
}

var wsNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,31}$`)

func (s *Service) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var body struct {
		Name       string `json:"name"`
		TemplateID string `json:"template_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !wsNameRe.MatchString(body.Name) {
		http.Error(w, "invalid workspace name", http.StatusBadRequest)
		return
	}
	tmpl := body.TemplateID
	if tmpl == "" {
		tmpl = s.Config.DefaultTemplate
	}
	if tmpl == "" {
		http.Error(w, "template_id required (or set DEFAULT_TEMPLATE)", http.StatusBadRequest)
		return
	}
	tid, err := s.Coder.FindTemplateID(r.Context(), tok, tmpl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	me, err := s.Coder.GetMe(r.Context(), tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	ws, err := s.Coder.CreateWorkspace(r.Context(), tok, me.Username, body.Name, tid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (s *Service) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "workspace id required", http.StatusBadRequest)
		return
	}
	build, err := s.Coder.DeleteWorkspace(r.Context(), tok, id)
	if err != nil {
		if he, ok := err.(*coderHTTPError); ok {
			http.Error(w, he.Message, he.Status)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"workspace_id": id,
		"build":        build,
	})
}

func (s *Service) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	tmpls, err := s.Coder.ListTemplates(r.Context(), tok)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": tmpls})
}

func (s *Service) handleAppSession(w http.ResponseWriter, r *http.Request) {
	email := emailFromCtx(r.Context())
	tok, err := s.ensureCoderToken(r.Context(), email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sess := s.Store.CreateSession(email, tok)
	mt := s.Store.IssueMiddleToken(email, tok)
	s.setSessionCookie(w, sess.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"middle_token": mt.Token,
	})
}
