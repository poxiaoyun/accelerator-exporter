package nodefacts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// IdleDisksAnnotation is owned by accelerator-exporter. Its value is a JSON
	// array of disks that are mounted on the node and are safe for platform use.
	IdleDisksAnnotation = "storage.xiaoshiai.cn/idle-disks"

	diskUpdateInterval = 30 * time.Second
)

// Disk describes one locally mounted disk that is available for platform
// services. MountPath is the path on the Kubernetes node, not a container path.
type Disk struct {
	ID             string `json:"id"`
	DevicePath     string `json:"devicePath"`
	MountPath      string `json:"mountPath"`
	Filesystem     string `json:"filesystem,omitempty"`
	TotalBytes     uint64 `json:"totalBytes"`
	FreeBytes      uint64 `json:"freeBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
}

// DiskDetector supplies the node-local inventory. Keeping it as an interface
// makes annotation publishing testable without a host filesystem.
type DiskDetector interface {
	Detect(context.Context) ([]Disk, error)
}

// HostDiskDetector reads the host filesystem mounted at Root. It reports
// non-system filesystem mounts backed by block devices. The detector never
// formats, mounts, or writes to a disk.
type HostDiskDetector struct {
	Root string
}

func (d HostDiskDetector) Detect(ctx context.Context) ([]Disk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := filepath.Clean(strings.TrimSpace(d.Root))
	if root == "." || root == "" {
		return nil, fmt.Errorf("host root is required")
	}
	data, err := os.ReadFile(filepath.Join(root, "proc", "self", "mountinfo"))
	if err != nil {
		return nil, fmt.Errorf("read host mountinfo: %w", err)
	}
	ids := deviceIDs(root)
	seen := map[string]struct{}{}
	result := make([]Disk, 0)
	for _, line := range strings.Split(string(data), "\n") {
		mount, ok := parseMountInfoLine(line)
		if !ok || !strings.HasPrefix(mount.Source, "/dev/") {
			continue
		}
		mountPath, ok := hostMountPath(mount.MountPoint, root)
		if !ok || isSystemMount(mountPath) {
			continue
		}
		deviceName := resolveBlockName(root, mount.MajorMinor, mount.Source)
		id := ids[deviceName]
		if id == "" {
			id = deviceName
		}
		if id == "" {
			continue
		}
		key := id + "\x00" + mountPath
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		stat, err := statfs(filepath.Join(root, strings.TrimPrefix(mountPath, "/")))
		if err != nil {
			continue
		}
		result = append(result, Disk{
			ID:             id,
			DevicePath:     mount.Source,
			MountPath:      mountPath,
			Filesystem:     mount.Filesystem,
			TotalBytes:     stat.total,
			FreeBytes:      stat.free,
			AvailableBytes: stat.available,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID == result[j].ID {
			return result[i].MountPath < result[j].MountPath
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

// hostMountPath converts a mount point from the current mount namespace into
// a path relative to the host root bind mount. For example, when the host root
// is mounted at /host-root, the host's /cache mount appears as /host-root/cache.
func hostMountPath(mountPoint, hostRoot string) (string, bool) {
	mountPoint = filepath.Clean(mountPoint)
	hostRoot = filepath.Clean(hostRoot)
	if hostRoot == "/" {
		return mountPoint, true
	}
	if mountPoint == hostRoot {
		return "/", true
	}
	prefix := hostRoot + string(filepath.Separator)
	if !strings.HasPrefix(mountPoint, prefix) {
		return "", false
	}
	return "/" + strings.TrimPrefix(strings.TrimPrefix(mountPoint, prefix), string(filepath.Separator)), true
}

type mountInfo struct {
	MajorMinor string
	MountPoint string
	Filesystem string
	Source     string
}

func parseMountInfoLine(line string) (mountInfo, bool) {
	parts := strings.SplitN(line, " - ", 2)
	if len(parts) != 2 {
		return mountInfo{}, false
	}
	left := strings.Fields(parts[0])
	right := strings.Fields(parts[1])
	if len(left) < 6 || len(right) < 2 {
		return mountInfo{}, false
	}
	return mountInfo{MajorMinor: left[2], MountPoint: decodeMountInfoPath(left[4]), Filesystem: right[0], Source: decodeMountInfoPath(right[1])}, true
}

func decodeMountInfoPath(value string) string {
	value = strings.ReplaceAll(value, `\040`, " ")
	value = strings.ReplaceAll(value, `\011`, "\t")
	value = strings.ReplaceAll(value, `\012`, "\n")
	return strings.ReplaceAll(value, `\134`, `\`)
}

func isSystemMount(mountPath string) bool {
	mountPath = filepath.Clean(mountPath)
	if mountPath == "/" {
		return true
	}
	for _, prefix := range []string{"/boot", "/etc", "/usr", "/var/lib/kubelet", "/var/lib/containerd", "/var/lib/docker", "/var/lib/crio", "/proc", "/sys", "/dev", "/run"} {
		if mountPath == prefix || strings.HasPrefix(mountPath, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func deviceIDs(root string) map[string]string {
	result := map[string]string{}
	entries, err := os.ReadDir(filepath.Join(root, "dev", "disk", "by-id"))
	if err != nil {
		return result
	}
	type candidate struct{ id, name string }
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(root, "dev", "disk", "by-id", entry.Name()))
		if err != nil {
			continue
		}
		name := filepath.Base(filepath.Clean(target))
		if name == "" {
			continue
		}
		candidates = append(candidates, candidate{id: entry.Name(), name: name})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return diskIDPriority(candidates[i].id) < diskIDPriority(candidates[j].id)
	})
	for _, item := range candidates {
		if _, exists := result[item.name]; !exists {
			result[item.name] = item.id
		}
	}
	return result
}

func diskIDPriority(value string) int {
	for index, prefix := range []string{"wwn-", "scsi-", "nvme-", "ata-", "virtio-", "usb-"} {
		if strings.HasPrefix(value, prefix) {
			return index
		}
	}
	return 100
}

func resolveBlockName(root, majorMinor, source string) string {
	link := filepath.Join(root, "sys", "dev", "block", majorMinor)
	if resolved, err := filepath.EvalSymlinks(link); err == nil {
		parts := strings.Split(filepath.ToSlash(resolved), "/")
		for index, part := range parts {
			if part == "block" && index+1 < len(parts) {
				return parts[index+1]
			}
		}
	}
	name := filepath.Base(source)
	name = strings.TrimSuffix(name, "p1")
	for len(name) > 1 && name[len(name)-1] >= '0' && name[len(name)-1] <= '9' {
		name = name[:len(name)-1]
	}
	return name
}

type filesystemStat struct{ total, free, available uint64 }

func statfs(path string) (filesystemStat, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return filesystemStat{}, err
	}
	blockSize := uint64(stat.Bsize)
	return filesystemStat{total: stat.Blocks * blockSize, free: stat.Bfree * blockSize, available: stat.Bavail * blockSize}, nil
}

// DiskPublisher reconciles the exporter-owned inventory annotation.
type DiskPublisher struct {
	nodes      nodeClient
	nodeName   string
	detector   DiskDetector
	logger     *slog.Logger
	updateWait time.Duration
	retryWait  time.Duration
}

func NewDiskPublisher(nodes nodeClient, nodeName string, detector DiskDetector, logger *slog.Logger) *DiskPublisher {
	return &DiskPublisher{nodes: nodes, nodeName: nodeName, detector: detector, logger: logger, updateWait: diskUpdateInterval, retryWait: time.Minute}
}

func (publisher *DiskPublisher) SetInterval(interval time.Duration) {
	if interval > 0 {
		publisher.updateWait = interval
	}
}

func (publisher *DiskPublisher) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			wait := publisher.updateWait
			if err := publisher.Publish(ctx); err != nil {
				publisher.logger.Error("publish node disk inventory", "node", publisher.nodeName, "error", err)
				wait = publisher.retryWait
			}
			timer.Reset(wait)
		}
	}
}

func (publisher *DiskPublisher) Publish(ctx context.Context) error {
	disks, err := publisher.detector.Detect(ctx)
	if err != nil {
		return err
	}
	data, err := json.Marshal(disks)
	if err != nil {
		return fmt.Errorf("marshal disk inventory: %w", err)
	}
	node, err := publisher.nodes.Get(ctx, publisher.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node %q: %w", publisher.nodeName, err)
	}
	if node.Annotations[IdleDisksAnnotation] == string(data) {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{IdleDisksAnnotation: string(data)}}})
	if err != nil {
		return fmt.Errorf("marshal disk annotation patch: %w", err)
	}
	if _, err := publisher.nodes.Patch(ctx, publisher.nodeName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch node %q disk inventory: %w", publisher.nodeName, err)
	}
	publisher.logger.Info("published node disk inventory", "node", publisher.nodeName, "disks", len(disks))
	return nil
}
