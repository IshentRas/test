# ws-publish (skeleton)

Single-file Go CLI for illustration: [`cmd/ws-publish/main.go`](cmd/ws-publish/main.go).

Walks OverlayFS `upperdir`, builds a commit on `.base_commit_sha`, then:

| Command | Action |
|---|---|
| `ws-publish share` | Tag `ws/<user>/<id>` → push to main GitLab |
| `ws-publish promote` | Branch `ws/<user>/<id>` → push (+ MR API TODO) |
| `ws-publish promote --from-tag ws/…` | Branch from existing share tag SHA |

## Real `git` binary?

**Yes (v0).** Same as replica-lab / reconciler — clone/commit/tag/push + user PAT via HTTPS URL. go-git optional later for a fully static image.

## Build

```bash
cd workspace-publish
go build -o ws-publish ./cmd/ws-publish
```

## Auth

`$GITLAB_TOKEN` in the disposable workspace — push **as that user**. Separate from replica `PUSH_AUTH_*`.
