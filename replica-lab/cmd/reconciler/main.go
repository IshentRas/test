package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/adr001/replica-lab/internal/k8sstate"
	"github.com/adr001/replica-lab/internal/layout"
)

func main() {
	exportRoot := env("EXPORT_ROOT", "/var/git-backend")
	cacheBare := filepath.Join(env("CACHE_ROOT", "/var/git-cache"), "repo.git")
	remote := env("GIT_REMOTE", "http://git-replica.adr001.svc.cluster.local:8080/repo.git")
	ns := env("NAMESPACE", "adr001")
	cmName := env("CONFIGMAP", "git-release-state")

	if err := os.MkdirAll(exportRoot, 0o755); err != nil {
		log.Fatalf("mkdir export: %v", err)
	}

	log.Printf("git-reconciler starting remote=%s export=%s", remote, exportRoot)

	reconcile := func(active, tags string) {
		if active == "" || active == "pending" {
			log.Printf("skip: ACTIVE_COMMIT=%q", active)
			return
		}
		if err := layout.FetchBare(cacheBare, remote); err != nil {
			log.Printf("fetch failed: %v", err)
			return
		}
		if err := layout.Materialize(cacheBare, exportRoot, active, tags); err != nil {
			log.Printf("materialize failed: %v", err)
			return
		}
		log.Printf("reconciled active=%s", active)
	}

	ctx := context.Background()
	k8s, err := k8sstate.New(ns, cmName)
	if err != nil {
		log.Fatalf("k8s client: %v", err)
	}
	active, tags, err := k8s.GetRelease(ctx)
	if err != nil {
		log.Fatalf("read configmap: %v", err)
	}
	reconcile(active, tags)

	if err := k8sstate.WatchLoop(ctx, ns, cmName, reconcile); err != nil {
		log.Fatalf("watch configmap: %v", err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
