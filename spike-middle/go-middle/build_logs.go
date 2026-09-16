package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// handleBuildLogs upgrades to WebSocket and pipes Coder provisioner logs for
// the workspace's latest build (or ?build= override).
//
// Upstream: GET /api/v2/workspacebuilds/{id}/logs?follow=true&after=…
func (s *Service) handleBuildLogs(w http.ResponseWriter, r *http.Request) {
	_, coderTok, status, msg := s.coderTokenFromAppRequest(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	wsID := r.PathValue("id")
	if wsID == "" {
		http.Error(w, "workspace id required", http.StatusBadRequest)
		return
	}

	buildID := r.URL.Query().Get("build")
	if buildID == "" {
		var err error
		buildID, err = s.findLatestBuildID(r, coderTok, wsID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	after := r.URL.Query().Get("after")
	if after == "" {
		after = "-1"
	}

	upstreamPath := "/api/v2/workspacebuilds/" + url.PathEscape(buildID) + "/logs"
	s.pipeCoderLogWS(w, r, coderTok, upstreamPath, after)
}

// handleAgentLogs streams startup / runtime agent logs (different API from
// provisioner build logs).
//
// Upstream: GET /api/v2/workspaceagents/{agent}/logs?follow=true&after=…
func (s *Service) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	_, coderTok, status, msg := s.coderTokenFromAppRequest(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	wsID := r.PathValue("id")
	if wsID == "" {
		http.Error(w, "workspace id required", http.StatusBadRequest)
		return
	}

	agentID := r.URL.Query().Get("agent")
	if agentID == "" {
		var err error
		agentID, err = s.findAgentID(r, coderTok, wsID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}

	after := r.URL.Query().Get("after")
	if after == "" {
		after = "0"
	}

	upstreamPath := "/api/v2/workspaceagents/" + url.PathEscape(agentID) + "/logs"
	s.pipeCoderLogWS(w, r, coderTok, upstreamPath, after)
}

func (s *Service) pipeCoderLogWS(w http.ResponseWriter, r *http.Request, coderTok, upstreamPath, after string) {
	clientWS, err := s.upgradeWS(w, r)
	if err != nil {
		return
	}
	defer clientWS.Close()

	u, err := url.Parse(s.Config.CoderURL)
	if err != nil {
		writeLogErr(clientWS, err.Error())
		return
	}
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	u.Path = upstreamPath
	u.RawQuery = "follow=true&after=" + url.QueryEscape(after)

	hdr := http.Header{}
	hdr.Set("Coder-Session-Token", coderTok)

	upstream, _, err := websocket.DefaultDialer.Dial(u.String(), hdr)
	if err != nil {
		writeLogErr(clientWS, "upstream dial: "+err.Error())
		return
	}
	defer upstream.Close()

	errc := make(chan error, 2)

	go func() {
		for {
			if _, _, err := clientWS.ReadMessage(); err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() {
		for {
			mt, data, err := upstream.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := clientWS.WriteMessage(mt, data); err != nil {
				errc <- err
				return
			}
		}
	}()

	<-errc
}

func writeLogErr(ws *websocket.Conn, msg string) {
	payload, _ := json.Marshal(map[string]any{
		"stage":     "error",
		"log_level": "error",
		"output":    msg,
	})
	_ = ws.WriteMessage(websocket.TextMessage, payload)
}

func (s *Service) findLatestBuildID(r *http.Request, coderTok, workspaceID string) (string, error) {
	raw, err := s.Coder.WorkspaceRaw(r.Context(), coderTok, workspaceID)
	if err != nil {
		return "", err
	}
	lb, _ := raw["latest_build"].(map[string]any)
	if lb == nil {
		return "", fmt.Errorf("no latest_build")
	}
	if id, ok := lb["id"].(string); ok && id != "" {
		return id, nil
	}
	return "", fmt.Errorf("latest_build missing id")
}
