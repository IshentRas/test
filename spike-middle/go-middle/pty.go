package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func (s *Service) upgradeWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	u := websocket.Upgrader{CheckOrigin: s.checkWSOrigin}
	return u.Upgrade(w, r, nil)
}

func (s *Service) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	_, _, status, msg := s.coderTokenFromAppRequest(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	id := r.PathValue("id")
	mt := r.URL.Query().Get("mt")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Terminal</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css"/>
<style>
  html, body { margin: 0; height: 100%%; background: #0b1220; color: #e8eefc; font-family: ui-sans-serif, system-ui, sans-serif; }
  #bar { display: flex; align-items: center; justify-content: space-between; gap: 12px;
    padding: 10px 14px; border-bottom: 1px solid #243049; background: #121a2b; font-size: 12px; }
  #status { font-family: ui-monospace, monospace; color: #93a0b8; }
  #term { height: calc(100%% - 42px); padding: 8px; box-sizing: border-box; }
  .xterm, .xterm-viewport { height: 100%% !important; }
</style>
</head>
<body>
<div id="bar">
  <span>Terminal — workspace %s</span>
  <span id="status">connecting…</span>
</div>
<div id="term"></div>
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/lib/addon-fit.min.js"></script>
<script>
(function () {
  const id = %q;
  const mt = %q;
  const statusEl = document.getElementById('status');
  const host = document.getElementById('term');
  const term = new Terminal({
    cursorBlink: true,
    fontFamily: 'IBM Plex Mono, ui-monospace, monospace',
    fontSize: 13,
    scrollback: 5000,
    theme: { background: '#0b1220', foreground: '#e8eefc', cursor: '#93c5fd' }
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(host);
  fit.fit();

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const qs = new URLSearchParams({
    mt: mt,
    height: String(term.rows),
    width: String(term.cols)
  });
  const ws = new WebSocket(proto + '://' + location.host + '/v1/workspaces/' + id + '/pty?' + qs.toString());
  ws.binaryType = 'arraybuffer';
  const enc = new TextEncoder();
  const dec = new TextDecoder();

  function sendResize() {
    if (ws.readyState !== WebSocket.OPEN) return;
    ws.send(enc.encode(JSON.stringify({ height: term.rows, width: term.cols })));
  }

  ws.onopen = function () {
    statusEl.textContent = 'connected';
    sendResize();
    term.focus();
  };
  ws.onclose = function () { statusEl.textContent = 'disconnected'; };
  ws.onerror = function () { statusEl.textContent = 'error'; };
  ws.onmessage = function (ev) {
    if (typeof ev.data === 'string') term.write(ev.data);
    else term.write(dec.decode(ev.data));
  };

  term.onData(function (data) {
    if (ws.readyState !== WebSocket.OPEN) return;
    ws.send(enc.encode(JSON.stringify({ data: data })));
  });

  window.addEventListener('resize', function () {
    fit.fit();
    sendResize();
  });
})();
</script>
</body></html>`, id, id, mt)
}

func (s *Service) handlePTY(w http.ResponseWriter, r *http.Request) {
	_, coderTok, status, msg := s.coderTokenFromAppRequest(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	wsID := r.PathValue("id")
	agentID, err := s.findAgentID(r, coderTok, wsID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	height := queryInt(r, "height", 40)
	width := queryInt(r, "width", 120)

	clientWS, err := s.upgradeWS(w, r)
	if err != nil {
		return
	}
	defer clientWS.Close()

	reconnect := uuid.NewString()
	u, err := url.Parse(s.Config.CoderURL)
	if err != nil {
		_ = clientWS.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	u.Path = "/api/v2/workspaceagents/" + agentID + "/pty"
	q := u.Query()
	q.Set("reconnect", reconnect)
	q.Set("height", strconv.Itoa(height))
	q.Set("width", strconv.Itoa(width))
	u.RawQuery = q.Encode()

	hdr := http.Header{}
	hdr.Set("Coder-Session-Token", coderTok)

	upstream, _, err := websocket.DefaultDialer.Dial(u.String(), hdr)
	if err != nil {
		_ = clientWS.WriteMessage(websocket.TextMessage, []byte("upstream dial: "+err.Error()))
		return
	}
	defer upstream.Close()

	errc := make(chan error, 2)

	// Browser → Coder agent. Client sends UTF-8 JSON:
	//   {"data":"..."} or {"height":N,"width":N}
	// Forward as binary JSON text frames (Coder agent expectation).
	go func() {
		for {
			_, data, err := clientWS.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			payload := normalizePTYClientFrame(data)
			if err := upstream.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				errc <- err
				return
			}
		}
	}()

	// Coder agent → browser: unwrap {"data":...} JSON when present, else pass through.
	go func() {
		for {
			mt, data, err := upstream.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			out := data
			outMT := mt
			if mt == websocket.TextMessage || mt == websocket.BinaryMessage {
				var msg struct {
					Data string `json:"data"`
				}
				if json.Unmarshal(data, &msg) == nil && msg.Data != "" {
					out = []byte(msg.Data)
					outMT = websocket.BinaryMessage
				}
			}
			if err := clientWS.WriteMessage(outMT, out); err != nil {
				errc <- err
				return
			}
		}
	}()

	<-errc
}

func normalizePTYClientFrame(data []byte) []byte {
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		// Plain keystrokes → wrap as data frame.
		wrapped, _ := json.Marshal(map[string]any{"data": string(data)})
		return wrapped
	}
	if _, ok := msg["data"]; ok {
		out, _ := json.Marshal(msg)
		return out
	}
	if _, ok := msg["height"]; ok {
		out, _ := json.Marshal(msg)
		return out
	}
	if _, ok := msg["width"]; ok {
		out, _ := json.Marshal(msg)
		return out
	}
	wrapped, _ := json.Marshal(map[string]any{"data": string(data)})
	return wrapped
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func (s *Service) findAgentID(r *http.Request, coderTok, workspaceID string) (string, error) {
	raw, err := s.Coder.WorkspaceRaw(r.Context(), coderTok, workspaceID)
	if err != nil {
		return "", err
	}
	agent := pickAgent(raw)
	if agent == nil {
		return "", fmt.Errorf("no agent on workspace (is it running?)")
	}
	id, _ := agent["id"].(string)
	if id == "" {
		return "", fmt.Errorf("no agent on workspace (is it running?)")
	}
	return id, nil
}
