import express from "express";
import crypto from "crypto";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PORT = Number(process.env.PORT || 5173);
const MIDDLE_URL = (process.env.MIDDLE_URL || "http://localhost:8081").replace(/\/$/, "");
const IDP_ISSUER = (process.env.OIDC_ISSUER_URL || "http://localhost:8080/realms/coder-lab").replace(
  /\/$/,
  ""
);
const CLIENT_ID = process.env.OIDC_CLIENT_ID || "middle-service";
const REDIRECT_URI =
  process.env.OIDC_REDIRECT_URI || `http://localhost:${PORT}/auth/callback`;
const PUBLIC_URL = (process.env.PORTAL_PUBLIC_URL || `http://localhost:${PORT}`).replace(/\/$/, "");

const pkceStore = new Map(); // state -> { verifier, expires }

function b64url(buf) {
  return Buffer.from(buf)
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function newPKCE() {
  const verifier = b64url(crypto.randomBytes(32));
  const challenge = b64url(crypto.createHash("sha256").update(verifier).digest());
  return { verifier, challenge };
}

function prunePKCE() {
  const now = Date.now();
  for (const [k, v] of pkceStore) {
    if (v.expires < now) pkceStore.delete(k);
  }
}

const app = express();
app.use(express.json());
app.use(express.static(path.join(__dirname, "public")));

app.get("/config.js", (_req, res) => {
  res.type("application/javascript").send(
    `window.SPIKE_CONFIG = ${JSON.stringify({
      middleUrl: MIDDLE_URL,
      idpIssuer: IDP_ISSUER,
      clientId: CLIENT_ID,
      redirectUri: REDIRECT_URI,
      publicUrl: PUBLIC_URL,
    })};`
  );
});

app.get("/auth/login", (_req, res) => {
  prunePKCE();
  const { verifier, challenge } = newPKCE();
  const state = b64url(crypto.randomBytes(24));
  pkceStore.set(state, { verifier, expires: Date.now() + 10 * 60 * 1000 });
  const q = new URLSearchParams({
    client_id: CLIENT_ID,
    response_type: "code",
    scope: "openid profile email",
    redirect_uri: REDIRECT_URI,
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  });
  res.redirect(`${IDP_ISSUER}/protocol/openid-connect/auth?${q}`);
});

app.get("/auth/callback", async (req, res) => {
  const { code, state, error, error_description: desc } = req.query;
  if (error) {
    res.status(400).send(`OIDC error: ${error} ${desc || ""}`);
    return;
  }
  const entry = pkceStore.get(String(state || ""));
  pkceStore.delete(String(state || ""));
  if (!entry || !code) {
    res.status(400).send("invalid or expired OIDC state");
    return;
  }
  try {
    const body = new URLSearchParams({
      grant_type: "authorization_code",
      client_id: CLIENT_ID,
      code: String(code),
      redirect_uri: REDIRECT_URI,
      code_verifier: entry.verifier,
    });
    const tokRes = await fetch(`${IDP_ISSUER}/protocol/openid-connect/token`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
    });
    const text = await tokRes.text();
    if (!tokRes.ok) {
      res.status(502).type("text").send(`token exchange failed: ${text}`);
      return;
    }
    const tokens = JSON.parse(text);
    // Hand tokens to the SPA via a short-lived HTML bridge (sessionStorage).
    res.type("html").send(`<!DOCTYPE html><html><body><script>
sessionStorage.setItem('spike_portal_auth', ${JSON.stringify(
      JSON.stringify({
        token: tokens.access_token,
        refresh: tokens.refresh_token || null,
        expires_in: tokens.expires_in || null,
        source: "keycloak",
      })
    )});
location.replace('/');
</script></body></html>`);
  } catch (e) {
    res.status(502).send(String(e.message || e));
  }
});

app.get("/auth/logout", (_req, res) => {
  const q = new URLSearchParams({
    client_id: CLIENT_ID,
    post_logout_redirect_uri: PUBLIC_URL + "/",
  });
  res.redirect(`${IDP_ISSUER}/protocol/openid-connect/logout?${q}`);
});

app.listen(PORT, () => {
  console.log(`spike portal http://localhost:${PORT}`);
  console.log(`middle URL   ${MIDDLE_URL}`);
  console.log(`idp issuer   ${IDP_ISSUER}`);
});
