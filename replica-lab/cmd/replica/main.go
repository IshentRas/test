package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adr001/replica-lab/internal/gitexec"
	"github.com/adr001/replica-lab/internal/k8sstate"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hook-post-receive" {
		if err := runPostReceiveHook(); err != nil {
			log.Fatalf("hook: %v", err)
		}
		return
	}
	if err := runServer(); err != nil {
		log.Fatalf("replica: %v", err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runServer() error {
	listen := env("LISTEN", ":8080")
	dataRoot := env("DATA_ROOT", "/data")
	repoName := env("REPO_NAME", "repo.git")
	upstream := env("UPSTREAM_URL", "")
	ns := env("NAMESPACE", "adr001")
	cmName := env("CONFIGMAP", "git-release-state")
	mainRef := env("MAIN_REF", "refs/heads/main")

	repoPath := filepath.Join(dataRoot, repoName)
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		return err
	}
	if err := ensureBare(repoPath, upstream); err != nil {
		return err
	}
	if err := installPostReceiveHook(repoPath); err != nil {
		return err
	}

	k8s, err := k8sstate.New(ns, cmName)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := publishHead(ctx, k8s, repoPath, mainRef); err != nil {
		log.Printf("warn: initial publish: %v", err)
	}

	backend, err := gitexec.HTTPBackendPath()
	if err != nil {
		return err
	}
	gitCGI := &cgi.Handler{
		Path: backend,
		Root: dataRoot,
		Env: []string{
			"GIT_PROJECT_ROOT=" + dataRoot,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/v1/sync-upstream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if upstream == "" {
			http.Error(w, "UPSTREAM_URL unset", http.StatusBadRequest)
			return
		}
		if err := gitexec.Run(repoPath, "fetch", "--tags", "--force", "origin",
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := publishHead(r.Context(), k8s, repoPath, mainRef); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("synced\n"))
	})
	// Smart HTTP via git-http-backend (same pattern as production).
	mux.Handle("/"+repoName+"/", gitCGI)

	log.Printf("git-replica listening on %s repo=%s upstream=%s backend=%s", listen, repoPath, upstream, backend)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}

func ensureBare(repoPath, upstream string) error {
	if _, err := os.Stat(filepath.Join(repoPath, "objects")); err == nil {
		if upstream != "" {
			if err := gitexec.Run(repoPath, "remote", "get-url", "origin"); err != nil {
				_ = gitexec.Run(repoPath, "remote", "add", "origin", upstream)
			}
		}
		return nil
	}
	if upstream == "" {
		return gitexec.Run("", "init", "--bare", "-b", "main", repoPath)
	}
	log.Printf("mirroring upstream %s -> %s", upstream, repoPath)
	return gitexec.Run("", "clone", "--bare", upstream, repoPath)
}

func installPostReceiveHook(repoPath string) error {
	hook := filepath.Join(repoPath, "hooks", "post-receive")
	body := "#!/bin/sh\nexec /usr/local/bin/git-replica hook-post-receive\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		return err
	}
	return nil
}

func runPostReceiveHook() error {
	ns := env("NAMESPACE", "adr001")
	cmName := env("CONFIGMAP", "git-release-state")
	repoPath := filepath.Join(env("DATA_ROOT", "/data"), env("REPO_NAME", "repo.git"))
	mainRef := env("MAIN_REF", "refs/heads/main")

	k8s, err := k8sstate.New(ns, cmName)
	if err != nil {
		return err
	}
	var mainSHA string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		ref, newSHA := fields[2], fields[1]
		if ref == mainRef && newSHA != strings.Repeat("0", 40) {
			mainSHA = newSHA
		}
	}
	if mainSHA == "" {
		return nil
	}
	return publishSHA(context.Background(), k8s, repoPath, mainSHA)
}

func publishHead(ctx context.Context, k8s *k8sstate.Client, repoPath, mainRef string) error {
	sha, err := gitexec.Output(repoPath, "rev-parse", mainRef)
	if err != nil {
		sha, err = gitexec.Output(repoPath, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
	}
	return publishSHA(ctx, k8s, repoPath, sha)
}

func publishSHA(ctx context.Context, k8s *k8sstate.Client, repoPath, sha string) error {
	tags, err := listTags(repoPath)
	if err != nil {
		return err
	}
	log.Printf("patching git-release-state ACTIVE_COMMIT=%s tags=%v", sha, tags)
	return k8s.PatchRelease(ctx, sha, tags)
}

func listTags(repoPath string) (map[string]string, error) {
	out, err := gitexec.Output(repoPath, "show-ref", "--tags", "-d")
	if err != nil {
		return map[string]string{}, nil
	}
	tags := map[string]string{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "^{}") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sha, ref := fields[0], fields[1]
		name := strings.TrimPrefix(ref, "refs/tags/")
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		tags[name] = sha
	}
	return tags, nil
}
