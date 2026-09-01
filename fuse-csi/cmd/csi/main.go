package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const driverName = "git.csi.adr001.local"

// Thin CSI node plugin: bind-mounts the long-lived FUSE export into pods.
// All mount ops run in the *host* mount namespace via nsenter so we never keep a
// stale ENOTCONN view of /var/git-fuse inside the CSI container.
type driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedNodeServer

	nodeID     string
	fuseExport string
	hostTools  string // directory on host with static busybox (+ optional tools)

	mu      sync.Mutex
	publish map[string]struct{}
}

func main() {
	endpoint := flag.String("endpoint", "unix:///var/lib/kubelet/plugins/git.csi.adr001.local/csi.sock", "CSI gRPC endpoint")
	nodeID := flag.String("node-id", "", "Kubernetes node name")
	fuseExport := flag.String("fuse-export", "/var/git-fuse", "host path of FUSE mount")
	hostTools := flag.String("host-tools", "/mnt/git-storage/bins", "host path with static busybox for nsenter (Bottlerocket-safe)")
	flag.Parse()

	if *nodeID == "" {
		*nodeID = os.Getenv("NODE_ID")
	}
	if *nodeID == "" {
		klog.Fatal("node-id is required")
	}
	if v := os.Getenv("ADR001_HOST_TOOLS"); v != "" {
		*hostTools = v
	}

	d := &driver{
		nodeID:     *nodeID,
		fuseExport: *fuseExport,
		hostTools:  *hostTools,
		publish:    map[string]struct{}{},
	}

	if err := d.run(*endpoint); err != nil {
		klog.Fatalf("csi server: %v", err)
	}
}

func (d *driver) run(endpoint string) error {
	scheme, addr, err := parseEndpoint(endpoint)
	if err != nil {
		return err
	}
	if scheme == "unix" {
		_ = os.Remove(addr)
		if err := os.MkdirAll(filepath.Dir(addr), 0o755); err != nil {
			return err
		}
	}
	lis, err := net.Listen(scheme, addr)
	if err != nil {
		return err
	}
	klog.Infof("CSI listening on %s (fuse export %s, host mount ns via nsenter)", endpoint, d.fuseExport)

	s := grpc.NewServer()
	csi.RegisterIdentityServer(s, d)
	csi.RegisterNodeServer(s, d)
	return s.Serve(lis)
}

func parseEndpoint(ep string) (string, string, error) {
	if strings.HasPrefix(ep, "unix://") {
		return "unix", strings.TrimPrefix(ep, "unix://"), nil
	}
	if strings.HasPrefix(ep, "tcp://") {
		return "tcp", strings.TrimPrefix(ep, "tcp://"), nil
	}
	return "", "", fmt.Errorf("unsupported endpoint %q", ep)
}

func (d *driver) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{Name: driverName, VendorVersion: "0.1.0"}, nil
}
func (d *driver) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{}, nil
}
func (d *driver) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}
func (d *driver) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{}, nil
}
func (d *driver) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: d.nodeID}, nil
}

func (d *driver) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" || req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id and target path required")
	}
	target := req.GetTargetPath()

	if err := d.requireHealthyExport(); err != nil {
		return nil, status.Errorf(codes.Unavailable, "fuse export unhealthy: %v", err)
	}

	// Clear any dead/previous bind at the target, then bind from the live host export.
	d.hostLazyUnmount(target)
	if err := d.hostRun("mkdir", "-p", target); err != nil {
		return nil, status.Errorf(codes.Internal, "mkdir target: %v", err)
	}

	args := []string{"--bind"}
	if req.GetReadonly() {
		args = append(args, "-o", "ro")
	}
	args = append(args, d.fuseExport, target)
	if err := d.hostRun(append([]string{"mount"}, args...)...); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount: %v", err)
	}
	if err := d.requireReadable(target); err != nil {
		d.hostLazyUnmount(target)
		return nil, status.Errorf(codes.Internal, "published path not readable: %v", err)
	}

	d.mu.Lock()
	d.publish[target] = struct{}{}
	d.mu.Unlock()
	klog.Infof("NodePublishVolume bind %s -> %s", d.fuseExport, target)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *driver) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	target := req.GetTargetPath()
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target path required")
	}
	d.hostLazyUnmount(target)
	_ = d.hostRun("rmdir", target)
	d.mu.Lock()
	delete(d.publish, target)
	d.mu.Unlock()
	klog.Infof("NodeUnpublishVolume %s", target)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (d *driver) requireHealthyExport() error {
	return d.requireReadable(d.fuseExport)
}

func (d *driver) requireReadable(path string) error {
	// Run stat+ls in host mount ns via static busybox — never trust CSI container FUSE view,
	// and never invoke Bottlerocket host brush/sh.
	out, err := d.hostOutput("sh", "-c", fmt.Sprintf(
		`set -e; test -d '%s'; ls '%s' >/dev/null; test -e '%s/CURRENT_SHA' -o -n "$(ls -A '%s' 2>/dev/null)"`,
		path, path, path, path,
	))
	if err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *driver) hostLazyUnmount(target string) {
	_ = d.hostRun("umount", "-l", target)
}

func (d *driver) hostRun(args ...string) error {
	_, err := d.hostOutput(args...)
	return err
}

func (d *driver) hostOutput(args ...string) ([]byte, error) {
	busybox := filepath.Join(d.hostTools, "busybox")
	// Prefer static busybox on the host path (works under Bottlerocket nsenter).
	// Fall back to bare args for environments where host tools exist (kind/AL2023).
	var cmd *exec.Cmd
	if _, err := os.Stat(busybox); err == nil {
		cmdArgs := append([]string{"--mount=/proc/1/ns/mnt", "--", busybox}, args...)
		cmd = exec.Command("nsenter", cmdArgs...)
	} else {
		cmdArgs := append([]string{"--mount=/proc/1/ns/mnt", "--"}, args...)
		cmd = exec.Command("nsenter", cmdArgs...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("nsenter %v: %v (%s)", args, err, bytes.TrimSpace(out))
	}
	return out, nil
}

func isENOTCONN(err error) bool {
	return errors.Is(err, syscall.ENOTCONN)
}

func (d *driver) NodeStageVolume(context.Context, *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (d *driver) NodeUnstageVolume(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (d *driver) NodeGetVolumeStats(context.Context, *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (d *driver) NodeExpandVolume(context.Context, *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
