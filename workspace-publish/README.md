# ws-publish (skeleton)

Go CLI baked into the **workspace image**. Walks OverlayFS `upperdir`, builds a commit on `.base_commit_sha`, then:

| Command | Action |
|---|---|
| `ws-publish share` | Tag `ws/<user>/<id>` → push to main GitLab |
| `ws-publish promote` | Branch `ws/<user>/<id>` → push (+ MR API TODO) |
| `ws-publish promote --from-tag ws/…` | Branch from existing share tag SHA (no upper rebuild) |

## Does it need the real `git` binary?

**Yes (v0).** Same choice as `replica-lab` / reconciler:

- `git clone` / `checkout` / `commit` / `tag` / `branch` / `push` are well-trodden
- Credential URL / askpass for user PAT is trivial
- Whiteout → `rm` + `git add -A` is simpler than hand-building trees in go-git

**Optional later:** go-git for a fully static binary with no `git` in the image. Not required for the skeleton.

Workspace image should include `git` (UBI/Alpine package) next to `ws-publish`.

## Auth

Uses the disposable user token (`GITLAB_TOKEN` / `-token`) to push **as that user** to main GitLab. Separate from replica `PUSH_AUTH_*`.

## Build

```bash
cd workspace-publish
go build -o ws-publish ./cmd/ws-publish
```

## Status

Skeleton only — walk upper, commit, tag/branch push wired; GitLab MR API and hardened whiteout/opaque-dir handling are TODO.
