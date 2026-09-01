package main

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Quest FUSE server: read-only loopback over the reconciler backend.
// Process is intentionally separate from the CSI gRPC plugin.
func main() {
	backend := flag.String("backend", "/var/git-backend", "reconciler materialization root")
	mountpoint := flag.String("mountpoint", "/var/git-fuse", "FUSE mount path published to CSI")
	flag.Parse()

	if err := os.MkdirAll(*backend, 0o755); err != nil {
		log.Fatalf("mkdir backend: %v", err)
	}

	// Clear a leftover / dead FUSE mount from a previous process before remounting.
	clearStaleMount(*mountpoint)
	if err := os.MkdirAll(*mountpoint, 0o755); err != nil {
		// Still ENOTCONN? force lazy unmount again.
		_ = exec.Command("umount", "-l", *mountpoint).Run()
		if err2 := os.MkdirAll(*mountpoint, 0o755); err2 != nil {
			log.Fatalf("mkdir mountpoint: %v", err2)
		}
	}

	// Wait until reconciler has produced at least CURRENT_SHA.
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(filepath.Join(*backend, "CURRENT_SHA")); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	root, err := fs.NewLoopbackRoot(*backend)
	if err != nil {
		log.Fatalf("loopback root: %v", err)
	}

	// DirectMountStrict: mount via /dev/fuse without host fusermount(3).
	// Required on Bottlerocket (immutable OS — no dnf install fuse packages).
	// Safe on AL2023/kind when we already run privileged in the host mount ns.
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther:         true,
			Options:            []string{"ro", "default_permissions"},
			FsName:             "adr001-git",
			Name:               "adr001git",
			DisableXAttrs:      true,
			DirectMount:        true,
			DirectMountStrict:  true,
		},
	}

	server, err := fs.Mount(*mountpoint, root, opts)
	if err != nil {
		log.Fatalf("mount: %v", err)
	}
	log.Printf("FUSE mounted %s -> backend %s (pid=%d)", *mountpoint, *backend, os.Getpid())

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		log.Printf("signal received; unmounting %s", *mountpoint)
		_ = server.Unmount()
	}()

	server.Wait()
	log.Printf("FUSE server exited")
}

func clearStaleMount(mountpoint string) {
	// If mountpoint is already a (possibly dead) FUSE mount, detach it.
	_ = exec.Command("fusermount", "-uz", mountpoint).Run()
	_ = exec.Command("umount", "-l", mountpoint).Run()
	// mkdir may fail with ENOTCONN if the dentry is still poisoned; caller retries.
}
