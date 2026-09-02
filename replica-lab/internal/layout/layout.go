package layout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adr001/replica-lab/internal/gitexec"
)

func Materialize(bareRepo, exportRoot, activeSHA, tagsJSON string) error {
	if activeSHA == "" || activeSHA == "pending" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(exportRoot, "commits"), 0o755); err != nil {
		return err
	}
	if err := materializeCommit(bareRepo, exportRoot, activeSHA); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(exportRoot, "CURRENT_SHA"), []byte(activeSHA), 0o644); err != nil {
		return err
	}
	if err := symlinkRel("commits/"+activeSHA, filepath.Join(exportRoot, "current")); err != nil {
		return err
	}
	tags := map[string]string{}
	if tagsJSON != "" {
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
			return fmt.Errorf("ACTIVE_TAGS json: %w", err)
		}
	}
	if err := syncTags(exportRoot, tags); err != nil {
		return err
	}
	for _, sha := range tags {
		if sha == "" {
			continue
		}
		if err := materializeCommit(bareRepo, exportRoot, sha); err != nil {
			return err
		}
	}
	return chmodExport(exportRoot)
}

func materializeCommit(bareRepo, exportRoot, sha string) error {
	dest := filepath.Join(exportRoot, "commits", sha)
	if st, err := os.Stat(filepath.Join(dest, "config", "VERSION")); err == nil && !st.IsDir() {
		return nil
	}
	_ = os.RemoveAll(dest)
	tmp, err := os.MkdirTemp(filepath.Join(exportRoot, "commits"), ".tmp."+sha+".")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("git", "-C", bareRepo, "archive", sha)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	tar := exec.Command("tar", "-x", "-C", tmp)
	tar.Stdin, err = cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var tarErr bytes.Buffer
	tar.Stderr = &tarErr
	if err := tar.Start(); err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive %s: %w", sha, err)
	}
	if err := tar.Wait(); err != nil {
		return fmt.Errorf("tar extract: %w (%s)", err, strings.TrimSpace(tarErr.String()))
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	return nil
}

func syncTags(exportRoot string, tags map[string]string) error {
	tagsDir := filepath.Join(exportRoot, "tags")
	if err := os.MkdirAll(tagsDir, 0o755); err != nil {
		return err
	}
	wanted := map[string]struct{}{}
	for name, sha := range tags {
		wanted[name] = struct{}{}
		if err := symlinkRel("../commits/"+sha, filepath.Join(tagsDir, name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(tagsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, ok := wanted[e.Name()]; !ok {
			_ = os.Remove(filepath.Join(tagsDir, e.Name()))
		}
	}
	return nil
}

func symlinkRel(target, linkpath string) error {
	_ = os.Remove(linkpath)
	return os.Symlink(target, linkpath)
}

func chmodExport(exportRoot string) error {
	return filepath.Walk(exportRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0o755)
		}
		return os.Chmod(path, 0o644)
	})
}

func FetchBare(cacheBare, remote string) error {
	if _, err := os.Stat(filepath.Join(cacheBare, "objects")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(cacheBare), 0o755); err != nil {
			return err
		}
		return gitexec.Run("", "clone", "--bare", remote, cacheBare)
	}
	return gitexec.Run(cacheBare, "fetch", "--tags", "--force", "origin",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
}
