package gitexec

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Run invokes the real git binary. Same approach as replica-lab/reconciler:
// push, commit, and whiteout→rm mapping are simpler with CLI git than go-git.
func Run(dir string, args ...string) error {
	_, err := Output(dir, args...)
	return err
}

func Output(dir string, args ...string) (string, error) {
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
