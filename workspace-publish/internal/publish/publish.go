package publish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adr001/workspace-publish/internal/gitexec"
	"github.com/adr001/workspace-publish/internal/upper"
)

// Config drives share / promote. Skeleton: wires upper walk → git commit → tag/branch push.
type Config struct {
	ScratchRoot string // PVC mount: upper/, work/, .base_commit_sha
	WorkDir     string // ephemeral git worktree (emptyDir or /tmp)
	RemoteURL   string // main GitLab HTTPS URL (token injected via URL or GIT_ASKPASS)
	User        string // for ws/<user>/… refs
	GitLabToken string // PAT / OAuth — never log
	BaseBranch  string // MR target, default main
}

func (c *Config) upperDir() string { return filepath.Join(c.ScratchRoot, "upper") }
func (c *Config) baseSHAFile() string {
	return filepath.Join(c.ScratchRoot, ".base_commit_sha")
}

func (c *Config) readBaseSHA() (string, error) {
	b, err := os.ReadFile(c.baseSHAFile())
	if err != nil {
		return "", fmt.Errorf("read base sha: %w", err)
	}
	sha := strings.TrimSpace(string(b))
	if sha == "" {
		return "", fmt.Errorf("empty .base_commit_sha")
	}
	return sha, nil
}

func (c *Config) refName() string {
	id := time.Now().UTC().Format("20060102-150405")
	user := sanitize(c.User)
	return fmt.Sprintf("ws/%s/%s", user, id)
}

func sanitize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// Share walks upperdir, commits on base, pushes tag refs/tags/ws/<user>/<id>.
func Share(c Config) (ref string, err error) {
	ref = c.refName()
	sha, err := c.buildCommit()
	if err != nil {
		return "", err
	}
	if err := gitexec.Run(c.WorkDir, "tag", ref, sha); err != nil {
		return "", err
	}
	if err := push(&c, "refs/tags/"+ref); err != nil {
		return "", err
	}
	return ref, nil
}

// Promote creates branch refs/heads/ws/<user>/<id> from upper (or fromTag SHA)
// and prints the intended MR action. GitLab MR API is TODO.
func Promote(c Config, fromTag string) (ref string, err error) {
	ref = c.refName()
	var sha string
	if fromTag != "" {
		if err := ensureRepo(&c); err != nil {
			return "", err
		}
		sha, err = gitexec.Output(c.WorkDir, "rev-parse", fromTag+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("resolve tag %s: %w", fromTag, err)
		}
	} else {
		sha, err = c.buildCommit()
		if err != nil {
			return "", err
		}
	}
	if err := gitexec.Run(c.WorkDir, "branch", ref, sha); err != nil {
		return "", err
	}
	if err := push(&c, "refs/heads/"+ref); err != nil {
		return "", err
	}
	// TODO: GitLab API create MR → c.BaseBranch using c.GitLabToken
	fmt.Fprintf(os.Stderr, "promote: pushed branch %s (MR API not implemented yet; target=%s)\n", ref, c.defaultBase())
	return ref, nil
}

func (c *Config) defaultBase() string {
	if c.BaseBranch == "" {
		return "main"
	}
	return c.BaseBranch
}

func (c *Config) buildCommit() (string, error) {
	base, err := c.readBaseSHA()
	if err != nil {
		return "", err
	}
	changes, err := upper.Walk(c.upperDir())
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", fmt.Errorf("upperdir empty — nothing to publish")
	}
	if err := ensureRepo(c); err != nil {
		return "", err
	}
	if err := gitexec.Run(c.WorkDir, "checkout", "--detach", base); err != nil {
		return "", fmt.Errorf("checkout base %s: %w", base, err)
	}
	if err := applyChanges(c.WorkDir, changes); err != nil {
		return "", err
	}
	if err := gitexec.Run(c.WorkDir, "add", "-A"); err != nil {
		return "", err
	}
	msg := "ws-publish: delta from overlay upperdir"
	if err := gitexec.Run(c.WorkDir, "-c", "user.email=ws-publish@local", "-c", "user.name=ws-publish",
		"commit", "--allow-empty", "-m", msg); err != nil {
		// allow-empty false preferred; if only deletes, add -A should still stage
		return "", err
	}
	return gitexec.Output(c.WorkDir, "rev-parse", "HEAD")
}

func ensureRepo(c *Config) error {
	if _, err := os.Stat(filepath.Join(c.WorkDir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(c.WorkDir, 0o755); err != nil {
		return err
	}
	url := remoteWithToken(c)
	// Shallow-ish clone of enough history to reach base SHA — skeleton uses full clone.
	return gitexec.Run("", "clone", url, c.WorkDir)
}

func applyChanges(workDir string, changes []upper.Change) error {
	for _, ch := range changes {
		dst := filepath.Join(workDir, filepath.FromSlash(ch.RelPath))
		if ch.Delete {
			_ = os.RemoveAll(dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(ch.AbsPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func push(c *Config, refspec string) error {
	url := remoteWithToken(c)
	// Skeleton: push by URL so we do not store the token in remote config.
	return gitexec.Run(c.WorkDir, "push", url, refspec)
}

func remoteWithToken(c *Config) string {
	if c.GitLabToken == "" {
		return c.RemoteURL
	}
	// https://oauth2:TOKEN@host/group/repo.git
	u := c.RemoteURL
	if strings.HasPrefix(u, "https://") {
		rest := strings.TrimPrefix(u, "https://")
		if strings.Contains(rest, "@") {
			return u
		}
		return "https://oauth2:" + c.GitLabToken + "@" + rest
	}
	return u
}
