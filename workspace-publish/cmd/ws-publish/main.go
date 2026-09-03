// ws-publish — illustrative single-file skeleton.
// Walks OverlayFS upperdir → git commit on .base_commit_sha → share (tag) or promote (branch).
// Requires the real git binary (same as replica-lab / reconciler). go-git optional later.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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

	fsArgs := flag.NewFlagSet(cmd, flag.ExitOnError)
	scratch := fsArgs.String("scratch", env("SCRATCH_ROOT", "/scratch"), "PVC root (upper/, .base_commit_sha)")
	work := fsArgs.String("workdir", env("PUBLISH_WORKDIR", "/tmp/ws-publish-git"), "ephemeral git clone/worktree")
	remote := fsArgs.String("remote", os.Getenv("GITLAB_REMOTE"), "main GitLab HTTPS repo URL")
	user := fsArgs.String("user", env("WS_USER", os.Getenv("USER")), "identity for ws/<user>/… refs")
	token := fsArgs.String("token", os.Getenv("GITLAB_TOKEN"), "PAT/OAuth (prefer env)")
	baseBranch := fsArgs.String("base-branch", env("GITLAB_BASE_BRANCH", "main"), "MR target")
	fromTag := fsArgs.String("from-tag", "", "promote: branch from existing share tag")
	_ = fsArgs.Parse(os.Args[2:])

	cfg := config{
		scratch:    *scratch,
		workDir:    *work,
		remote:     *remote,
		user:       *user,
		token:      *token,
		baseBranch: *baseBranch,
	}
	if cfg.remote == "" || cfg.user == "" {
		fmt.Fprintln(os.Stderr, "need -remote/$GITLAB_REMOTE and -user/$WS_USER")
		os.Exit(2)
	}

	var ref string
	var err error
	switch cmd {
	case "share":
		ref, err = share(cfg)
	case "promote":
		ref, err = promote(cfg, *fromTag)
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

type config struct {
	scratch, workDir, remote, user, token, baseBranch string
}

func share(c config) (string, error) {
	ref := refName(c.user)
	sha, err := buildCommit(c)
	if err != nil {
		return "", err
	}
	if err := git(c.workDir, "tag", ref, sha); err != nil {
		return "", err
	}
	if err := git(c.workDir, "push", remoteURL(c), "refs/tags/"+ref); err != nil {
		return "", err
	}
	return ref, nil
}

func promote(c config, fromTag string) (string, error) {
	ref := refName(c.user)
	var sha string
	var err error
	if fromTag != "" {
		if err := ensureRepo(c); err != nil {
			return "", err
		}
		sha, err = gitOut(c.workDir, "rev-parse", fromTag+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("resolve tag %s: %w", fromTag, err)
		}
	} else {
		sha, err = buildCommit(c)
		if err != nil {
			return "", err
		}
	}
	if err := git(c.workDir, "branch", ref, sha); err != nil {
		return "", err
	}
	if err := git(c.workDir, "push", remoteURL(c), "refs/heads/"+ref); err != nil {
		return "", err
	}
	target := c.baseBranch
	if target == "" {
		target = "main"
	}
	// TODO: GitLab API create MR → target using c.token
	fmt.Fprintf(os.Stderr, "promote: pushed branch %s (MR API TODO; target=%s)\n", ref, target)
	return ref, nil
}

func buildCommit(c config) (string, error) {
	base, err := readBaseSHA(c.scratch)
	if err != nil {
		return "", err
	}
	changes, err := walkUpper(filepath.Join(c.scratch, "upper"))
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", fmt.Errorf("upperdir empty — nothing to publish")
	}
	if err := ensureRepo(c); err != nil {
		return "", err
	}
	if err := git(c.workDir, "checkout", "--detach", base); err != nil {
		return "", fmt.Errorf("checkout base %s: %w", base, err)
	}
	for _, ch := range changes {
		dst := filepath.Join(c.workDir, filepath.FromSlash(ch.rel))
		if ch.del {
			_ = os.RemoveAll(dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		data, err := os.ReadFile(ch.abs)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", err
		}
	}
	if err := git(c.workDir, "add", "-A"); err != nil {
		return "", err
	}
	if err := git(c.workDir,
		"-c", "user.email=ws-publish@local", "-c", "user.name=ws-publish",
		"commit", "-m", "ws-publish: delta from overlay upperdir"); err != nil {
		return "", err
	}
	return gitOut(c.workDir, "rev-parse", "HEAD")
}

func ensureRepo(c config) error {
	if _, err := os.Stat(filepath.Join(c.workDir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(c.workDir, 0o755); err != nil {
		return err
	}
	return git("", "clone", remoteURL(c), c.workDir)
}

func readBaseSHA(scratch string) (string, error) {
	b, err := os.ReadFile(filepath.Join(scratch, ".base_commit_sha"))
	if err != nil {
		return "", fmt.Errorf("read base sha: %w", err)
	}
	sha := strings.TrimSpace(string(b))
	if sha == "" {
		return "", fmt.Errorf("empty .base_commit_sha")
	}
	return sha, nil
}

func refName(user string) string {
	user = strings.ReplaceAll(strings.TrimSpace(user), " ", "-")
	return fmt.Sprintf("ws/%s/%s", user, time.Now().UTC().Format("20060102-150405"))
}

func remoteURL(c config) string {
	if c.token == "" || !strings.HasPrefix(c.remote, "https://") {
		return c.remote
	}
	rest := strings.TrimPrefix(c.remote, "https://")
	if strings.Contains(rest, "@") {
		return c.remote
	}
	return "https://oauth2:" + c.token + "@" + rest
}

// --- upperdir walk (files + OverlayFS whiteouts) ---

type change struct {
	rel string // repo-relative slash path
	del bool
	abs string // path in upperdir (non-delete)
}

func walkUpper(upperdir string) ([]change, error) {
	var out []change
	err := filepath.WalkDir(upperdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(upperdir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(name, ".wh.") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if strings.HasPrefix(name, ".wh.") {
			target := strings.TrimPrefix(name, ".wh.")
			parent := filepath.ToSlash(filepath.Dir(rel))
			del := target
			if parent != "." {
				del = parent + "/" + target
			}
			out = append(out, change{rel: del, del: true})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if isWhiteout(info) {
			out = append(out, change{rel: relSlash, del: true})
			return nil
		}
		out = append(out, change{rel: relSlash, abs: path})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk upperdir: %w", err)
	}
	return out, nil
}

func isWhiteout(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0 && st.Rdev == 0
}

// --- thin git CLI wrapper ---

func git(dir string, args ...string) error {
	_, err := gitOut(dir, args...)
	return err
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Fprintf(os.Stderr, `ws-publish — overlay upperdir → GitLab tag (share) or branch/MR (promote)

Single-file illustration. Requires real git binary.

  ws-publish share   [flags]
  ws-publish promote [flags]

  -scratch -workdir -remote -user -token -base-branch -from-tag
  Env: SCRATCH_ROOT GITLAB_REMOTE WS_USER GITLAB_TOKEN

`)
}
