const cfg = window.SPIKE_CONFIG || { middleUrl: "http://localhost:8081" };
const KEY = "spike_portal_auth";

const COLORS = [
  "amber", "aqua", "azure", "coral", "crimson", "cyan", "emerald", "fuchsia",
  "gold", "indigo", "ivory", "jade", "lime", "magenta", "navy", "olive",
  "orange", "plum", "rose", "sapphire", "scarlet", "silver", "teal", "violet",
];
const ANIMALS = [
  "badger", "bear", "bird", "bison", "cat", "crane", "deer", "dolphin",
  "eagle", "falcon", "fox", "frog", "hawk", "heron", "koala", "lion",
  "lynx", "otter", "owl", "panda", "penguin", "rabbit", "raven", "seal",
  "shark", "sparrow", "tiger", "whale", "wolf", "zebra",
];

const el = {
  login: document.getElementById("view-login"),
  app: document.getElementById("view-app"),
  authStatus: document.getElementById("authStatus"),
  list: document.getElementById("list"),
  listError: document.getElementById("listError"),
  listLoading: document.getElementById("listLoading"),
  listEmpty: document.getElementById("listEmpty"),
  wsCount: document.getElementById("wsCount"),
  template: document.getElementById("template"),
  wsName: document.getElementById("wsName"),
  nameHint: document.getElementById("nameHint"),
  suggestName: document.getElementById("suggestName"),
  createPanel: document.getElementById("createPanel"),
  toggleCreate: document.getElementById("toggleCreate"),
  toggleCreateLabel: document.getElementById("toggleCreateLabel"),
  formError: document.getElementById("formError"),
  createLabel: document.getElementById("createLabel"),
  loginError: document.getElementById("loginError"),
  embed: document.getElementById("embed"),
  embedTitle: document.getElementById("embedTitle"),
  frame: document.getElementById("frame"),
  deleteDialog: document.getElementById("deleteDialog"),
  deleteName: document.getElementById("deleteName"),
  deleteError: document.getElementById("deleteError"),
  deleteConfirm: document.getElementById("deleteConfirm"),
};

const ICON_DISK = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="22" y1="12" x2="2" y2="12"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><line x1="6" y1="16" x2="6.01" y2="16"/><line x1="10" y1="16" x2="10.01" y2="16"/></svg>`;
const ICON_CHEVRON = `<svg class="chevron" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg>`;
const ICON_TERM = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>`;
const ICON_CODE = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>`;
const ICON_MONITOR = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>`;
const ICON_SSH = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 17v2a2 2 0 0 0 2 2h12"/><path d="M10 17V5a2 2 0 0 1 2-2h4"/><circle cx="6" cy="17" r="2"/><circle cx="16" cy="5" r="2"/></svg>`;
const ICON_APP = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/></svg>`;
const ICON_TRASH = `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>`;

let nameSuggestion = generateWorkspaceName();
let pollTimer = null;
let expandedId = null;
let busyCreate = false;
let busyDelete = false;
let pendingDelete = null; // { id, name }
/** @type {Record<string, any>} */
const accessCache = {};
/** @type {Record<string, { buildKey: string, mode: "build"|"agent"|"done", title: string, stage: string, lines: { text: string, level: string }[], socket: WebSocket|null, hideTimer: any, collapsed: boolean }>} */
const buildLogState = {};

function pick(arr) {
  return arr[Math.floor(Math.random() * arr.length)];
}

function generateWorkspaceName() {
  return `${pick(COLORS)}-${pick(ANIMALS)}-${Math.floor(Math.random() * 100)}`;
}

function loadAuth() {
  try {
    // OIDC callback writes sessionStorage bridge first.
    const bridge = sessionStorage.getItem(KEY);
    if (bridge) {
      sessionStorage.removeItem(KEY);
      localStorage.setItem(KEY, bridge);
    }
    const raw = JSON.parse(localStorage.getItem(KEY) || "null");
    if (!raw?.token) return null;
    const email = raw.email || emailFromJWT(raw.token);
    return { ...raw, email };
  } catch {
    return null;
  }
}

function saveAuth(a) {
  if (!a) localStorage.removeItem(KEY);
  else localStorage.setItem(KEY, JSON.stringify(a));
}

function emailFromJWT(token) {
  try {
    const payload = JSON.parse(atob(token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/")));
    return payload.email || payload.preferred_username || "";
  } catch {
    return "";
  }
}

async function api(path, opts = {}) {
  const auth = loadAuth();
  const headers = { ...(opts.headers || {}) };
  if (auth?.token) headers.Authorization = `Bearer ${auth.token}`;
  if (opts.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";
  const res = await fetch(`${cfg.middleUrl}${path}`, {
    ...opts,
    headers,
    credentials: "include",
  });
  const text = await res.text();
  let data;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!res.ok) throw new Error(typeof data === "string" ? data : JSON.stringify(data));
  return data;
}

function agentSnapshot(ws) {
  const resources = ws.latest_build?.resources || [];
  for (const r of resources) {
    for (const a of r.agents || []) {
      return {
        status: a.status || "",
        lifecycle: a.lifecycle_state || "",
      };
    }
  }
  return { status: "", lifecycle: "" };
}

function buildInfo(ws) {
  const b = ws.latest_build || {};
  const jobStatus = b.job?.status ? String(b.job.status) : "";
  // Provisioner progress lives on job.status; some Coder builds keep
  // latest_build.status as "running" after a successful start.
  const provisioner = jobStatus || String(b.status || "unknown");
  return {
    provisioner,
    rawStatus: String(b.status || ""),
    transition: String(b.transition || ""),
    buildNumber: b.build_number ?? b.BuildNumber ?? "—",
    agent: agentSnapshot(ws),
  };
}

function displayStatus(ws) {
  const b = buildInfo(ws);
  if (isActiveBuild(ws)) return b.provisioner;
  if (b.transition === "stop" && b.provisioner === "succeeded") return "stopped";
  if (isStarted(ws)) {
    if (b.agent.status === "connected") return "running";
    if (b.agent.lifecycle && b.agent.lifecycle !== "ready") return b.agent.lifecycle;
    return "running";
  }
  return b.provisioner || b.rawStatus || "unknown";
}

function statusTone(label) {
  switch (label) {
    case "succeeded":
    case "running":
    case "ready":
      return "badge--ok";
    case "pending":
    case "starting":
    case "creating":
      return "badge--warn";
    case "failed":
    case "canceled":
      return "badge--danger";
    default:
      return "badge--muted";
  }
}

function isActiveBuild(ws) {
  const s = buildInfo(ws).provisioner;
  return s === "pending" || s === "running";
}

function isStarted(ws) {
  const b = buildInfo(ws);
  if (b.transition !== "start") return false;
  if (b.provisioner === "succeeded") return true;
  if (b.rawStatus === "succeeded") return true;
  return false;
}

function needsPolling(workspaces) {
  return workspaces.some((ws) => {
    if (isActiveBuild(ws)) return true;
    const b = buildInfo(ws);
    if (b.transition === "delete" && b.provisioner !== "succeeded" && b.provisioner !== "failed") return true;
    const life = b.agent.lifecycle;
    return isStarted(ws) && life && life !== "ready";
  });
}

function openDeleteDialog(ws) {
  pendingDelete = { id: ws.id, name: ws.name };
  el.deleteName.textContent = ws.name;
  el.deleteError.classList.add("hidden");
  el.deleteDialog.classList.remove("hidden");
}

function closeDeleteDialog() {
  if (busyDelete) return;
  pendingDelete = null;
  el.deleteDialog.classList.add("hidden");
}

async function confirmDelete() {
  if (!pendingDelete || busyDelete) return;
  busyDelete = true;
  el.deleteConfirm.textContent = "Deleting…";
  el.deleteConfirm.disabled = true;
  el.deleteError.classList.add("hidden");
  try {
    await api(`/v1/workspaces/${pendingDelete.id}`, { method: "DELETE" });
    if (expandedId === pendingDelete.id) expandedId = null;
    delete accessCache[pendingDelete.id];
    pendingDelete = null;
    el.deleteDialog.classList.add("hidden");
    await refreshList();
  } catch (e) {
    el.deleteError.textContent = String(e.message || e);
    el.deleteError.classList.remove("hidden");
  } finally {
    busyDelete = false;
    el.deleteConfirm.textContent = "Delete workspace";
    el.deleteConfirm.disabled = false;
  }
}

function connectionStatus(ws) {
  const b = buildInfo(ws);
  if (isActiveBuild(ws)) return "Build in progress";
  if (isStarted(ws)) {
    if (b.agent.lifecycle && b.agent.lifecycle !== "ready") return "Preparing tools…";
    return "Ready to connect";
  }
  if (b.transition === "stop" && b.provisioner === "succeeded") return "Stopped";
  return "Not available";
}

function updateSuggestUI() {
  el.suggestName.textContent = nameSuggestion;
  const empty = !el.wsName.value.trim();
  el.nameHint.classList.toggle("hidden", !empty);
}

function setCreateOpen(open) {
  el.createPanel.classList.toggle("hidden", !open);
  el.toggleCreate.setAttribute("aria-expanded", open ? "true" : "false");
  el.toggleCreateLabel.textContent = open ? "Close setup" : "New workspace";
}

function renderAuth() {
  const auth = loadAuth();
  if (!auth) {
    el.login.classList.remove("hidden");
    el.app.classList.add("hidden");
    el.embed.classList.add("hidden");
    stopPoll();
    return;
  }
  el.login.classList.add("hidden");
  el.app.classList.remove("hidden");
  el.authStatus.textContent = auth.email;
}

async function ensureAppSession() {
  const data = await api("/v1/app-session", { method: "POST", body: "{}" });
  return data.middle_token;
}

async function loadAccess(wsId) {
  const data = await api(`/v1/workspaces/${wsId}/access`);
  accessCache[wsId] = data;
  return data;
}

function choiceIcon(kind) {
  switch (kind) {
    case "terminal":
      return ICON_TERM;
    case "vscode_browser":
      return ICON_CODE;
    case "vscode_desktop":
      return ICON_MONITOR;
    case "ssh":
      return ICON_SSH;
    default:
      return ICON_APP;
  }
}

function accessButtons(ws, access, { compact = false } = {}) {
  const choices = access?.choices || [
    { id: "terminal", kind: "terminal", label: "Terminal", available: isStarted(ws) },
  ];
  return choices
    .map((c) => {
      const disabled = !c.available;
      const title = disabled ? c.unavailable_reason || "Not available" : c.label;
      const cls = `btn btn--access${compact ? " btn--icon" : ""}`;
      return `<button type="button" class="${cls}"
        data-action="choice" data-kind="${escapeAttr(c.kind)}" data-slug="${escapeAttr(c.slug || "")}"
        data-id="${escapeAttr(ws.id)}" data-name="${escapeAttr(ws.name)}"
        ${disabled ? "disabled" : ""} title="${escapeAttr(title)}">
        ${choiceIcon(c.kind)}${compact ? "" : `<span>${escapeHtml(c.label)}</span>`}
      </button>`;
    })
    .join("");
}

function buildKey(ws) {
  const b = ws.latest_build || {};
  return `${b.id || ""}:${b.build_number ?? ""}`;
}

function agentStarting(ws) {
  if (!isStarted(ws)) return false;
  const access = accessCache[ws.id];
  if (access?.startup_ready) return false;
  const life = buildInfo(ws).agent.lifecycle;
  if (life === "ready") return false;
  // After provision succeeds, show agent logs until ready (or briefly while access loads).
  return true;
}

function logPanelMode(ws) {
  if (isActiveBuild(ws)) return "build";
  if (agentStarting(ws)) return "agent";
  return null;
}

function shouldShowLogPanel(ws) {
  const st = buildLogState[ws.id];
  if (logPanelMode(ws)) return true;
  // Keep after finish so the user can expand and re-read.
  return !!(st && st.lines.length && st.buildKey === buildKey(ws));
}

function buildLogHTML(ws) {
  if (!shouldShowLogPanel(ws)) return "";
  const mode = logPanelMode(ws) || buildLogState[ws.id]?.mode || "done";
  const st = buildLogState[ws.id];
  const collapsed = st?.collapsed === true && !logPanelMode(ws);
  const title =
    mode === "agent" || (mode === "done" && st?.mode === "agent") || (st?.lines || []).some((l) => l.text.includes("Agent startup"))
      ? "Logs"
      : mode === "build"
        ? "Build logs"
        : "Logs";
  const stage =
    st?.stage ||
    (mode === "agent" ? "Starting agent…" : mode === "build" ? "Connecting…" : "Ready");
  const count = st?.lines?.length || 0;
  return `
    <div class="build-log${collapsed ? " is-collapsed" : ""}" data-build-log="${escapeAttr(ws.id)}">
      <button type="button" class="build-log-head" data-action="toggle-logs" data-id="${escapeAttr(ws.id)}" aria-expanded="${collapsed ? "false" : "true"}">
        <p>${escapeHtml(title)}${collapsed && count ? ` · ${count} lines` : ""}</p>
        <span class="build-log-head-right">
          <span class="status">${escapeHtml(stage)}</span>
          <svg class="build-log-chevron" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg>
        </span>
      </button>
      <pre class="build-log-body"></pre>
    </div>`;
}

function paintBuildLog(wsId) {
  const root = el.list.querySelector(`[data-build-log="${CSS.escape(wsId)}"]`);
  if (!root) return;
  const st = buildLogState[wsId];
  if (!st) return;
  const collapsed = st.collapsed === true && !st.socket;
  root.classList.toggle("is-collapsed", collapsed);
  const headBtn = root.querySelector(".build-log-head");
  if (headBtn) headBtn.setAttribute("aria-expanded", collapsed ? "false" : "true");
  const title = root.querySelector(".build-log-head p");
  const status = root.querySelector(".status");
  const body = root.querySelector(".build-log-body");
  const count = st.lines.length;
  if (title) {
    const base = st.title || "Logs";
    title.textContent = collapsed && count ? `${base} · ${count} lines` : base;
  }
  if (status) status.textContent = st.stage || "";
  if (!body) return;
  body.innerHTML = st.lines
    .map((line) => {
      const cls =
        line.level === "error"
          ? "build-log-line--error"
          : line.level === "warn"
            ? "build-log-line--warn"
            : line.level === "stage"
              ? "build-log-line--stage"
              : "";
      return `<span class="${cls}">${escapeHtml(line.text)}</span>`;
    })
    .join("\n");
  if (!collapsed) body.scrollTop = body.scrollHeight;
}

function paintAllBuildLogs() {
  for (const id of Object.keys(buildLogState)) paintBuildLog(id);
}

function closeBuildLogSocket(wsId) {
  const st = buildLogState[wsId];
  if (st?.hideTimer) {
    clearTimeout(st.hideTimer);
    st.hideTimer = null;
  }
  if (st?.socket) {
    try {
      st.socket.close();
    } catch {
      /* ignore */
    }
    st.socket = null;
  }
}

function middleWSURL(path, params) {
  const base = cfg.middleUrl.replace(/\/$/, "");
  const proto = base.startsWith("https") ? "wss" : "ws";
  const host = base.replace(/^https?:\/\//, "");
  const q = new URLSearchParams(params);
  return `${proto}://${host}${path}?${q}`;
}

function pushLogLines(st, lines) {
  for (const line of lines) {
    if (!line?.text) continue;
    st.lines.push(line);
  }
  if (st.lines.length > 2500) st.lines.splice(0, st.lines.length - 2500);
}

function ingestBuildMessage(st, raw) {
  let log;
  try {
    log = JSON.parse(raw);
  } catch {
    pushLogLines(st, [{ text: String(raw), level: "info" }]);
    return;
  }
  if (log.stage && log.stage !== st.stage) {
    st.stage = log.stage;
    pushLogLines(st, [{ text: `── ${log.stage} ──`, level: "stage" }]);
  }
  if (log.output != null && String(log.output).length) {
    pushLogLines(st, [{ text: String(log.output), level: log.log_level || "info" }]);
  }
}

function ingestAgentMessage(st, raw) {
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    pushLogLines(st, [{ text: String(raw), level: "info" }]);
    return;
  }
  const batch = Array.isArray(parsed) ? parsed : [parsed];
  const lines = [];
  for (const log of batch) {
    if (log?.output === "" && log?.id === 0) continue;
    if (log?.output == null || !String(log.output).length) continue;
    lines.push({ text: String(log.output), level: log.level || log.log_level || "info" });
  }
  if (lines.length) {
    st.stage = "Startup";
    pushLogLines(st, lines);
  }
}

async function openLogSocket(ws, mode) {
  const key = buildKey(ws);
  let st = buildLogState[ws.id];
  if (!st || st.buildKey !== key) {
    closeBuildLogSocket(ws.id);
    st = {
      buildKey: key,
      mode,
      title: mode === "agent" ? "Agent logs" : "Build logs",
      stage: "Connecting…",
      lines: [],
      socket: null,
      hideTimer: null,
      collapsed: false,
    };
    buildLogState[ws.id] = st;
  } else if (st.mode !== mode) {
    closeBuildLogSocket(ws.id);
    st.mode = mode;
    st.title = mode === "agent" ? "Agent logs" : mode === "build" ? "Build logs" : "Logs";
    st.stage = mode === "agent" ? "Starting agent…" : "Connecting…";
    st.collapsed = false;
    if (mode === "agent" && st.lines.length) {
      st.title = "Logs";
      pushLogLines(st, [{ text: "── Agent startup ──", level: "stage" }]);
    } else if (mode === "build") {
      st.lines = [];
    }
  } else {
    st.collapsed = false;
  }
  if (st.socket && (st.socket.readyState === WebSocket.OPEN || st.socket.readyState === WebSocket.CONNECTING)) {
    return;
  }
  let mt;
  try {
    mt = await ensureAppSession();
  } catch (e) {
    st.stage = `Auth error: ${e.message || e}`;
    paintBuildLog(ws.id);
    return;
  }
  const params = { mt, after: mode === "agent" ? "0" : "-1" };
  let path;
  if (mode === "build") {
    path = `/v1/workspaces/${ws.id}/build-logs`;
    if (ws.latest_build?.id) params.build = ws.latest_build.id;
  } else {
    path = `/v1/workspaces/${ws.id}/agent-logs`;
  }
  const sock = new WebSocket(middleWSURL(path, params));
  st.socket = sock;
  sock.onopen = () => {
    if (!st.stage || st.stage === "Connecting…") {
      st.stage = mode === "agent" ? "Startup" : "Streaming…";
    }
    paintBuildLog(ws.id);
  };
  sock.onmessage = (ev) => {
    if (mode === "agent") ingestAgentMessage(st, ev.data);
    else ingestBuildMessage(st, ev.data);
    paintBuildLog(ws.id);
  };
  sock.onerror = () => {
    st.stage = "Log stream error";
    paintBuildLog(ws.id);
  };
  sock.onclose = () => {
    st.socket = null;
    if (st.stage === "Connecting…" || st.stage === "Streaming…" || st.stage === "Startup") {
      st.stage = "Finished";
    }
    paintBuildLog(ws.id);
  };
}

function scheduleCollapseLogPanel(wsId) {
  const st = buildLogState[wsId];
  if (!st || st.collapsed) return;
  if (st.hideTimer) clearTimeout(st.hideTimer);
  st.mode = "done";
  st.title = "Logs";
  st.stage = "Ready";
  paintBuildLog(wsId);
  st.hideTimer = setTimeout(() => {
    closeBuildLogSocket(wsId);
    st.collapsed = true;
    st.hideTimer = null;
    paintBuildLog(wsId);
  }, 1500);
}

function syncBuildLogStreams(workspaces) {
  for (const ws of workspaces) {
    const mode = logPanelMode(ws);
    if (mode) {
      openLogSocket(ws, mode).catch(() => {});
      continue;
    }
    const st = buildLogState[ws.id];
    if (!st || !st.lines.length) continue;
    if (st.collapsed) {
      closeBuildLogSocket(ws.id);
      continue;
    }
    const ready =
      isStarted(ws) &&
      (accessCache[ws.id]?.startup_ready || buildInfo(ws).agent.lifecycle === "ready");
    if (ready || (!isActiveBuild(ws) && !agentStarting(ws))) {
      scheduleCollapseLogPanel(ws.id);
    }
  }
  paintAllBuildLogs();
}

function escapeAttr(s) {
  return String(s).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function renderList(workspaces) {
  const sorted = [...workspaces].sort((a, b) => a.name.localeCompare(b.name));
  el.wsCount.textContent = `${sorted.length} total`;
  el.listLoading.classList.add("hidden");
  el.listEmpty.classList.toggle("hidden", sorted.length > 0);
  el.list.innerHTML = "";

  for (const ws of sorted) {
    const b = buildInfo(ws);
    const label = displayStatus(ws);
    const open = expandedId === ws.id;
    const access = accessCache[ws.id];
    const li = document.createElement("li");
    li.className = `ws-card${open ? " is-open" : ""}`;
    li.dataset.id = ws.id;
    const statusLine = access
      ? access.startup_ready
        ? "Ready to connect"
        : access.started
          ? "Preparing tools…"
          : connectionStatus(ws)
      : connectionStatus(ws);
    li.innerHTML = `
      <div class="ws-card-row">
        <button type="button" class="ws-toggle" data-action="toggle" data-id="${ws.id}" aria-expanded="${open}">
          <span class="ws-icon">${ICON_DISK}</span>
          <span class="ws-meta">
            <span class="ws-title-row">
              <span class="ws-name">${escapeHtml(ws.name)}</span>
              <span class="badge ${statusTone(label)}">${escapeHtml(label)}</span>
              ${b.transition ? `<span class="ws-transition">${escapeHtml(b.transition)}</span>` : ""}
            </span>
            <span class="ws-sub">Build #${escapeHtml(String(b.buildNumber))} · ${escapeHtml(ws.template_name || ws.template_id || "template")}</span>
          </span>
          ${ICON_CHEVRON}
        </button>
        <div class="ws-actions">
          ${!open && isStarted(ws) ? accessButtons(ws, access, { compact: true }) : ""}
          <button type="button" class="btn btn--danger btn--sm" data-action="delete"
            data-id="${escapeAttr(ws.id)}" data-name="${escapeAttr(ws.name)}"
            ${isActiveBuild(ws) || busyDelete ? "disabled" : ""}
            title="${isActiveBuild(ws) ? "Build in progress" : "Delete workspace"}">
            ${ICON_TRASH}<span class="btn-label">Delete</span>
          </button>
        </div>
      </div>
      <div class="ws-detail">
        ${buildLogHTML(ws)}
        <div class="access-box">
          <div class="access-box-head">
            <p>Open with</p>
            <span class="status">${escapeHtml(statusLine)}</span>
          </div>
          <div class="access-actions">${accessButtons(ws, access)}</div>
        </div>
        <p class="ws-detail-meta">${escapeHtml(ws.id)}${(access?.display_apps || []).length ? ` · display_apps: ${escapeHtml((access.display_apps || []).join(", "))}` : ""}</p>
      </div>
    `;
    el.list.appendChild(li);
  }

  schedulePoll(sorted);
  syncBuildLogStreams(sorted);
}

function schedulePoll(workspaces) {
  stopPoll();
  if (needsPolling(workspaces)) {
    pollTimer = setTimeout(() => refreshList({ quiet: true }).catch(() => {}), 2500);
  }
}

function stopPoll() {
  if (pollTimer) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}

async function refreshList({ quiet = false } = {}) {
  if (!quiet) {
    el.listError.classList.add("hidden");
    if (!el.list.children.length) el.listLoading.classList.remove("hidden");
  }
  try {
    const data = await api("/v1/workspaces");
    const workspaces = data.workspaces || [];
    await Promise.all(
      workspaces.filter((ws) => isStarted(ws)).map((ws) => loadAccess(ws.id).catch(() => null))
    );
    renderList(workspaces);
  } catch (e) {
    el.listLoading.classList.add("hidden");
    el.listError.textContent = String(e.message || e);
    el.listError.classList.remove("hidden");
    if (!quiet) throw e;
  }
}

async function loadTemplates() {
  const data = await api("/v1/templates");
  el.template.innerHTML = "";
  for (const t of data.templates || []) {
    const opt = document.createElement("option");
    opt.value = t.id;
    opt.textContent = t.display_name || t.name;
    el.template.appendChild(opt);
  }
  if (!el.template.options.length) {
    const opt = document.createElement("option");
    opt.value = "";
    opt.textContent = "(no templates — set DEFAULT_TEMPLATE)";
    el.template.appendChild(opt);
  }
}

async function openTerminal(wsId, name) {
  const mt = await ensureAppSession();
  el.embed.classList.remove("hidden");
  el.embedTitle.textContent = `Terminal — ${name}`;
  el.frame.src = `${cfg.middleUrl}/v1/workspaces/${wsId}/terminal?mt=${encodeURIComponent(mt)}`;
}

async function openApp(wsId, name, slug) {
  const mt = await ensureAppSession();
  el.embed.classList.remove("hidden");
  el.embedTitle.textContent = `${slug || "App"} — ${name}`;
  // Bootstrap via /open so cookie is set and mt= never lands on folder=
  el.frame.src = `${cfg.middleUrl}/v1/workspaces/${wsId}/open/${encodeURIComponent(slug || "code-server")}?mt=${encodeURIComponent(mt)}`;
}

async function openVSCodeDesktop(wsId) {
  const data = await api(`/v1/workspaces/${wsId}/vscode-desktop`, { method: "POST", body: "{}" });
  if (!data?.uri) throw new Error("no vscode desktop uri");
  window.location.href = data.uri;
}

async function copySSH(name) {
  const cmd = `coder ssh ${name}`;
  try {
    await navigator.clipboard.writeText(cmd);
    alert(`Copied: ${cmd}\n\nUse after: coder login ${cfg.middleUrl}`);
  } catch {
    prompt("SSH via middle (after coder login):", cmd);
  }
}

async function runChoice({ kind, slug, id, name }) {
  if (kind === "terminal") return openTerminal(id, name);
  if (kind === "vscode_desktop") return openVSCodeDesktop(id);
  if (kind === "ssh") return copySSH(name);
  if (kind === "vscode_browser" || kind === "app") return openApp(id, name, slug || "code-server");
  throw new Error(`unsupported access kind: ${kind}`);
}

async function handleAuthError() {
  const params = new URLSearchParams(location.search);
  const err = params.get("error");
  if (err) {
    el.loginError.textContent = err;
    el.loginError.classList.remove("hidden");
    history.replaceState({}, "", "/");
  }
}

document.getElementById("logout").onclick = () => {
  saveAuth(null);
  setCreateOpen(false);
  location.href = "/auth/logout";
};

document.getElementById("refresh").onclick = () =>
  refreshList().catch((e) => {
    el.listError.textContent = e.message;
    el.listError.classList.remove("hidden");
  });

document.getElementById("toggleCreate").onclick = () => {
  setCreateOpen(el.createPanel.classList.contains("hidden"));
};

document.getElementById("emptyCreate").onclick = () => setCreateOpen(true);

document.getElementById("suggestName").onclick = () => {
  el.wsName.value = nameSuggestion;
  nameSuggestion = generateWorkspaceName();
  updateSuggestUI();
};

el.wsName.addEventListener("input", updateSuggestUI);

document.getElementById("createForm").onsubmit = async (ev) => {
  ev.preventDefault();
  if (busyCreate) return;
  el.formError.classList.add("hidden");
  busyCreate = true;
  el.createLabel.textContent = "Launching…";
  try {
    const ws = await api("/v1/workspaces", {
      method: "POST",
      body: JSON.stringify({
        name: el.wsName.value.trim(),
        template_id: el.template.value || undefined,
      }),
    });
    el.wsName.value = "";
    nameSuggestion = generateWorkspaceName();
    updateSuggestUI();
    setCreateOpen(false);
    expandedId = ws.id;
    await refreshList();
  } catch (e) {
    el.formError.textContent = e.message;
    el.formError.classList.remove("hidden");
  } finally {
    busyCreate = false;
    el.createLabel.textContent = "Create workspace";
  }
};

el.list.addEventListener("click", async (ev) => {
  const btn = ev.target.closest("[data-action]");
  if (!btn) return;
  const { action, id, name, kind, slug } = btn.dataset;
  try {
    if (action === "toggle-logs") {
      const st = buildLogState[id];
      if (!st) return;
      st.collapsed = !st.collapsed;
      paintBuildLog(id);
      return;
    }
    if (action === "toggle") {
      expandedId = expandedId === id ? null : id;
      if (expandedId) await loadAccess(expandedId).catch(() => null);
      const data = await api("/v1/workspaces");
      renderList(data.workspaces || []);
      return;
    }
    if (action === "delete") {
      openDeleteDialog({ id, name });
      return;
    }
    if (action === "choice") await runChoice({ kind, slug, id, name });
  } catch (e) {
    alert(String(e.message || e));
  }
});

document.getElementById("deleteCancel").onclick = () => closeDeleteDialog();
document.getElementById("deleteConfirm").onclick = () => confirmDelete();
el.deleteDialog.addEventListener("click", (ev) => {
  if (ev.target === el.deleteDialog) closeDeleteDialog();
});

document.getElementById("closeEmbed").onclick = () => {
  el.embed.classList.add("hidden");
  el.frame.src = "about:blank";
};

document.getElementById("brandHome").onclick = (ev) => {
  ev.preventDefault();
  el.embed.classList.add("hidden");
  el.frame.src = "about:blank";
};

updateSuggestUI();
handleAuthError();
renderAuth();
if (loadAuth()) {
  loadTemplates()
    .then(() => refreshList())
    .catch((e) => {
      el.listError.textContent = `middle error: ${e.message}`;
      el.listError.classList.remove("hidden");
    });
}
