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

	"github.com/adr001/replica-lab/internal/auth"
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

	repoPath := filepath.Join(dataRoot, repoName)
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		return err
	}
	if err := ensureBare(repoPath); err != nil {
		return err
	}
	if err := installPostReceiveHook(repoPath); err != nil {
		return err
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
	// Smart HTTP: GitLab (or lab mirror job) pushes here; reconcilers fetch via upload-pack.
	gitHandler := http.Handler(gitCGI)
	if creds, ok := auth.LoadPushCredentials(); ok {
		gitHandler = auth.PushAuthMiddleware(creds, gitHandler)
		log.Printf("push auth enabled (user=%q); fetch remains open", creds.User)
	} else {
		log.Print("push auth disabled — set PUSH_AUTH_USER and PUSH_AUTH_PASSWORD for GitLab mirror")
	}
	mux.Handle("/"+repoName+"/", gitHandler)

	log.Printf("git-replica listening on %s repo=%s (empty until first push)", listen, repoPath)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}

func ensureBare(repoPath string) error {
	if _, err := os.Stat(filepath.Join(repoPath, "objects")); err == nil {
		return nil
	}
	log.Printf("initializing empty bare repo at %s", repoPath)
	return gitexec.Run("", "init", "--bare", "-b", "main", repoPath)
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
