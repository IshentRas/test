# Middle + portal POC (Keycloak SSO → Coder)

POC folder for the **middle service** in front of Coder OSS:

| Piece | Path | Port |
| --- | --- | --- |
| Go middle | [`go-middle/`](go-middle/) | `:8081` |
| Node portal | [`portal/`](portal/) | `:5173` |
| Config example | [`config.example.yaml`](config.example.yaml) | — |
| Run script | [`scripts/run-middle.sh`](scripts/run-middle.sh) | — |

Related lab IdP: [`../local-sso/`](../local-sso/) (Keycloak `:8080`).  
ADR: [`../docs/adr/0001-agent-jobs-via-middle-service-and-standard-workspaces.md`](../docs/adr/0001-agent-jobs-via-middle-service-and-standard-workspaces.md).

**Coder’s own OIDC is not required** — middle uses an Owner API token against Coder.

## What it proves

1. Portal users sign in with **Keycloak** (OIDC Auth Code + PKCE).
2. Middle validates IdP JWTs (JWKS), finds/creates the Coder user (Owner), mints a user token.
3. Portal lists/creates workspaces, streams **build + agent logs**, opens Terminal / VS Code / SSH via middle.
4. `coder login http://localhost:8081` works (CLI illusion via `/cli-auth` + `/api/v2` + DERP).

## Layout

```text
spike-middle/
├── README.md                 ← this file
├── config.example.yaml       # copy → config.yaml (gitignored)
├── go-middle/                # Go middle (:8081)
│   ├── main.go               # routes
│   ├── config.go / auth.go   # YAML + JWKS auth
│   ├── handlers.go / coder.go
│   ├── access.go / proxy.go  # apps, /api/v2, DERP, /bin
│   ├── pty.go / build_logs.go
│   ├── cli_auth.go / tokens.go
│   └── …
├── portal/                   # Node portal (:5173)
│   ├── server.js             # OIDC login/callback
│   └── public/               # UI (workspaces, logs, apps)
└── scripts/run-middle.sh
```

## Quick start

### 1. Keycloak

```bash
cd local-sso
docker-compose up -d
# Admin: http://localhost:8080  (admin / admin)
# Realm coder-lab — alice/alice, bob/bob — client middle-service
```

### 2. Middle

```bash
cd spike-middle
cp config.example.yaml config.yaml   # optional
export CODER_OWNER_TOKEN='…'         # or file: /tmp/coder-spike-owner-token
./scripts/run-middle.sh
```

Env overrides: `CODER_URL`, `CODER_OWNER_TOKEN`, `LISTEN`, `MIDDLE_PUBLIC_URL`, `DEFAULT_TEMPLATE`, `OIDC_ISSUER_URL`, `CONFIG_FILE`.

### 3. Portal

```bash
cd spike-middle/portal
export MIDDLE_URL=http://localhost:8081
export OIDC_ISSUER_URL=http://localhost:8080/realms/coder-lab
npm install && npm start
```

Open http://localhost:5173 → **Continue with Keycloak**.

### 4. CLI (optional)

```bash
coder login http://localhost:8081
# /cli-auth → Login with Keycloak → copy mt_… → paste into CLI
coder whoami && coder ls
```

Use middle as `CODER_URL`, not `:3000`.

## Success checklist

| # | Check | Expected |
| --- | --- | --- |
| 1 | Keycloak login | Portal lists workspaces |
| 2 | Create workspace | Build logs stream, then agent logs; panel collapses when ready |
| 3 | Terminal | iframe → middle PTY |
| 4 | VS Code Browser | `/proxy/.../apps/code-server/` on middle |
| 5 | VS Code Desktop / SSH | Deep link + DERP via middle |
| 6 | Delete workspace | Starts delete build |
| 7 | `coder login` | Opaque `mt_…`; whoami/ls work |
| 8 | Owner token | Never shown in portal or CLI auth page |

## Auth flow

```text
User → Portal /auth/login → Keycloak (PKCE)
     → /auth/callback → access_token
     → API Authorization: Bearer <access_token>
Middle → JWKS (iss/exp/sig) → email
      → Owner find/create user → mint Coder token
Apps/logs → middle_session cookie or ?mt= (not IdP JWT in iframes)
```

Build logs: `GET /v1/workspaces/{id}/build-logs` → Coder `workspacebuilds/.../logs`  
Agent logs: `GET /v1/workspaces/{id}/agent-logs` → Coder `workspaceagents/.../logs`

## Config

See [`config.example.yaml`](config.example.yaml).

| Key / env | Purpose |
| --- | --- |
| `coder.url` / `CODER_URL` | Upstream Coder |
| `coder.owner_token` / `CODER_OWNER_TOKEN` | Owner token (prefer env) |
| `coder.default_template` / `DEFAULT_TEMPLATE` | Create template |
| `idp.issuer` / `OIDC_ISSUER_URL` | IdP issuer |
| `idp.jwks_url` / `OIDC_JWKS_URL` | Default `{issuer}/protocol/openid-connect/certs` |
| `idp.audience` / `OIDC_AUDIENCE` | Optional; leave empty for Keycloak |
| `public_url` / `MIDDLE_PUBLIC_URL` | Advertised middle URL |
| `portal_origins` | CORS + WebSocket allow-list |

## POC against a real org Coder

1. `cp config.example.yaml config.yaml`
2. Set `coder.url` to the org Coder HTTPS URL
3. Export `CODER_OWNER_TOKEN` (do not commit)
4. Set `idp.issuer` to the corporate IdP issuer
5. OIDC public client redirects:
   - `https://<portal>/auth/callback`
   - `https://<middle>/cli-auth/callback`
6. Set `public_url` + `portal_origins` to deployed origins
7. Smoke: SSO → create → logs → terminal / code-server / `coder login`

**Proxy notes:** strip inbound `Cookie` on app proxy; proxy `/bin/` for VS Code Desktop; DERP rewrite for `coder ssh`.

## Local lab defaults

| Piece | Value |
| --- | --- |
| Coder | `http://localhost:3000` |
| Keycloak | `http://localhost:8080` / realm `coder-lab` |
| Middle | `http://localhost:8081` |
| Portal | `http://localhost:5173` |
| Users | `alice`/`alice`, `bob`/`bob` |

## Out of scope (this POC)

- CI `/v1/runs` / Claude agent jobs (see ADR)
- Wiring Coder’s dashboard OIDC to Keycloak (optional via `local-sso`, not required for middle)
- Production Keycloak HA
