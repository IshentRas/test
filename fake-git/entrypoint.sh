#!/usr/bin/env bash
# Fake GitLab stand-in: bare repo over dumb HTTP + state.json for the quest.
set -euo pipefail

HTTP_ROOT=/git/http
BARE="$HTTP_ROOT/repo.git"
WORK=/git/work
META=/git/meta

rm -rf "$HTTP_ROOT" "$WORK" "$META"
mkdir -p "$BARE" "$WORK" "$META"

git init --bare -b main "$BARE"
git -C "$BARE" config uploadpack.allowAnySHA1InWant true

git clone "$BARE" "$WORK"
cd "$WORK"
git config user.email "quest@example.com"
git config user.name "ADR001 Quest"

mkdir -p config
echo "commit-a" > config/VERSION
echo "hello from A" > config/message.txt
git add config
git commit -m "commit A"
SHA_A="$(git rev-parse HEAD)"
git tag v1.0.0

echo "commit-b" > config/VERSION
echo "hello from B" > config/message.txt
git add config
git commit -m "commit B"
SHA_B="$(git rev-parse HEAD)"

git push origin main
git push origin v1.0.0

# Dumb HTTP needs info/refs + objects/info/packs
git -C "$BARE" update-server-info

cat > "$META/state.json" <<EOF
{
  "sha_a": "$SHA_A",
  "sha_b": "$SHA_B",
  "tag_v1_0_0": "$SHA_A",
  "active_commit": "$SHA_A",
  "active_tags": {"v1.0.0": "$SHA_A"}
}
EOF
cp "$META/state.json" "$HTTP_ROOT/state.json"

echo "fake-git ready sha_a=$SHA_A sha_b=$SHA_B"
echo "serving dumb HTTP git at http://0.0.0.0:8080/repo.git"

exec python3 - <<'PY'
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import os

os.chdir("/git/http")

class Handler(SimpleHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print("[http]", fmt % args)

    def end_headers(self):
        self.send_header("Cache-Control", "no-cache")
        super().end_headers()

ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
PY
