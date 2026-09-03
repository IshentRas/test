package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/adr001/workspace-publish/internal/publish"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		usage()
		return
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	scratch := fs.String("scratch", env("SCRATCH_ROOT", "/scratch"), "PVC root (upper/, .base_commit_sha)")
	work := fs.String("workdir", env("PUBLISH_WORKDIR", "/tmp/ws-publish-git"), "ephemeral git clone/worktree")
	remote := fs.String("remote", os.Getenv("GITLAB_REMOTE"), "main GitLab HTTPS repo URL")
	user := fs.String("user", env("WS_USER", os.Getenv("USER")), "identity for ws/<user>/… refs")
	token := fs.String("token", os.Getenv("GITLAB_TOKEN"), "PAT/OAuth token (prefer env)")
	baseBranch := fs.String("base-branch", env("GITLAB_BASE_BRANCH", "main"), "MR target branch")
	fromTag := fs.String("from-tag", "", "promote: create branch from existing share tag SHA")
	_ = fs.Parse(os.Args[2:])

	cfg := publish.Config{
		ScratchRoot: *scratch,
		WorkDir:     *work,
		RemoteURL:   *remote,
		User:        *user,
		GitLabToken: *token,
		BaseBranch:  *baseBranch,
	}
	if cfg.RemoteURL == "" {
		fmt.Fprintln(os.Stderr, "missing -remote or GITLAB_REMOTE")
		os.Exit(2)
	}
	if cfg.User == "" {
		fmt.Fprintln(os.Stderr, "missing -user or WS_USER")
		os.Exit(2)
	}

	var (
		ref string
		err error
	)
	switch cmd {
	case "share":
		ref, err = publish.Share(cfg)
	case "promote":
		ref, err = publish.Promote(cfg, *fromTag)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ws-publish %s: %v\n", cmd, err)
		os.Exit(1)
	}
	fmt.Printf("ok %s ref=%s\n", cmd, ref)
	if cmd == "share" {
		fmt.Printf("CONFIG_PATH hint: /mnt/git/tags/%s\n", ref)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Fprintf(os.Stderr, `ws-publish — overlay upperdir → GitLab tag (share) or branch/MR (promote)

Requires the real git binary (same as replica-lab / reconciler).

Usage:
  ws-publish share   [flags]
  ws-publish promote [flags]

Flags:
  -scratch     PVC root with upper/ and .base_commit_sha (default $SCRATCH_ROOT or /scratch)
  -workdir     ephemeral git work dir (default /tmp/ws-publish-git)
  -remote      GitLab HTTPS URL ($GITLAB_REMOTE)
  -user        ws/<user>/… ($WS_USER)
  -token       PAT/OAuth ($GITLAB_TOKEN) — prefer env, do not log
  -base-branch MR target (default main)
  -from-tag    promote from existing share tag without rebuilding upper

`)
}
