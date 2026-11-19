package nodefacts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeDiskDetector struct {
	disks []Disk
}

func (d fakeDiskDetector) Detect(context.Context) ([]Disk, error) { return d.disks, nil }

func TestDiskPublisherWritesInventoryAnnotation(t *testing.T) {
	client := &fakeNodeClient{node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}}
	publisher := NewDiskPublisher(client, "node-a", fakeDiskDetector{disks: []Disk{{ID: "wwn-test", MountPath: "/data", Interface: "nvme"}}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := publisher.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	var disks []Disk
	if err := json.Unmarshal([]byte(client.node.Annotations[IdleDisksAnnotation]), &disks); err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 || disks[0].ID != "wwn-test" || disks[0].MountPath != "/data" || disks[0].Interface != "nvme" {
		t.Fatalf("unexpected disk annotation: %#v", disks)
	}
	if err := publisher.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.patchCalls != 1 {
		t.Fatalf("patch calls = %d, want 1", client.patchCalls)
	}
}

func TestParseMountInfoLine(t *testing.T) {
	line := "36 25 8:1 / /data rw,relatime - ext4 /dev/sda1 rw"
	parsed, ok := parseMountInfoLine(line)
	if !ok {
		t.Fatal("mountinfo line was not parsed")
	}
	if parsed.MajorMinor != "8:1" || parsed.MountPoint != "/data" || parsed.Source != "/dev/sda1" {
		t.Fatalf("unexpected mount info: %#v", parsed)
	}
}

func TestHostMountPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mount     string
		root      string
		want      string
		wantValid bool
	}{
		{name: "host root mount", mount: "/host-root", root: "/host-root", want: "/", wantValid: true},
		{name: "host disk mount", mount: "/host-root/cache", root: "/host-root", want: "/cache", wantValid: true},
		{name: "nested host disk mount", mount: "/host-root/rune/warmup", root: "/host-root", want: "/rune/warmup", wantValid: true},
		{name: "outside host root", mount: "/cache", root: "/host-root", wantValid: false},
		{name: "root host", mount: "/cache", root: "/", want: "/cache", wantValid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := hostMountPath(test.mount, test.root)
			if got != test.want || valid != test.wantValid {
				t.Fatalf("hostMountPath(%q, %q) = (%q, %t), want (%q, %t)", test.mount, test.root, got, valid, test.want, test.wantValid)
			}
		})
	}
}

func TestIsSystemMountFiltersSnap(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/snap", "/snap/core24/2124"} {
		if !isSystemMount(path) {
			t.Errorf("isSystemMount(%q) = false, want true", path)
		}
	}
	if isSystemMount("/cache") {
		t.Fatal("isSystemMount(/cache) = true, want false")
	}
}

func TestDiskInterface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		disk  string
		paths []string
		want  string
	}{
		{name: "nvme", disk: "nvme0n1", paths: []string{"pci-0000:03:00.0-nvme-1"}, want: "nvme"},
		{name: "sata", disk: "sda", paths: []string{"pci-0000:00:17.0-ata-1", "pci-0000:00:17.0-ata-1-part1"}, want: "sata"},
		{name: "sas", disk: "sdb", paths: []string{"pci-0000:04:00.0-sas-phy0-lun-0"}, want: "scsi"},
		{name: "scsi", disk: "sdc", paths: []string{"pci-0000:00:05.0-scsi-0:0:0:0"}, want: "scsi"},
		{name: "usb", disk: "sdd", paths: []string{"pci-0000:00:14.0-usb-0:1:1.0-scsi-0:0:0:0"}, want: "unknown"},
		{name: "fibre channel", disk: "sde", paths: []string{"pci-0000:00:00.0-fc-0x5000-lun-0"}, want: "unknown"},
		{name: "iscsi", disk: "sdf", paths: []string{"ip-192.0.2.1:3260-iscsi-iqn.2020-01.example-lun-0"}, want: "unknown"},
		{name: "no path", disk: "sdg", want: "unknown"},
		{name: "virtio", disk: "vda", want: "virtio"},
		{name: "xen", disk: "xvda", want: "virtio"},
		{name: "loop", disk: "loop0", want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := diskInterface(test.disk, test.paths); got != test.want {
				t.Fatalf("diskInterface(%q) = %q, want %q", test.disk, got, test.want)
			}
		})
	}
}

func TestWholeDiskName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"nvme0n1":   "nvme0n1",
		"nvme0n1p1": "nvme0n1",
		"sda1":      "sda",
		"vda2":      "vda",
		"xvda1":     "xvda",
	}
	for name, want := range tests {
		got, ok := wholeDisk(name)
		if !ok || got != want {
			t.Errorf("wholeDisk(%q) = (%q, %t), want (%q, true)", name, got, ok, want)
		}
	}
	if got := kernelDiskName("nvme0n", "/dev/nvme0n1p1"); got != "nvme0n1" {
		t.Fatalf("kernelDiskName fallback = %q, want nvme0n1", got)
	}
}

func TestHostDiskDetectorReportsInterface(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"proc/self", "data", "cache", "scratch", "dev/disk/by-path"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mountinfo := "" +
		"1 0 8:1 / " + filepath.Join(root, "data") + " rw - ext4 /dev/sda1 rw\n" +
		"2 0 259:2 / " + filepath.Join(root, "cache") + " rw - ext4 /dev/nvme0n1p1 rw\n" +
		"3 0 8:17 / " + filepath.Join(root, "scratch") + " rw - ext4 /dev/sdb1 rw\n"
	if err := os.WriteFile(filepath.Join(root, "proc/self/mountinfo"), []byte(mountinfo), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"pci-0000:00:17.0-ata-1":                    "../../sda1",
		"pci-0000:03:00.0-nvme-1":                   "../../nvme0n1",
		"pci-0000:00:14.0-usb-0:1:1.0-scsi-0:0:0:0": "../../sdb",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, "dev/disk/by-path", name)); err != nil {
			t.Fatal(err)
		}
	}

	disks, err := HostDiskDetector{Root: root}.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, disk := range disks {
		got[disk.MountPath] = disk.Interface
	}
	want := map[string]string{"/data": "sata", "/cache": "nvme", "/scratch": "unknown"}
	if len(got) != len(want) {
		t.Fatalf("interfaces = %#v, want %#v", got, want)
	}
	for mount, iface := range want {
		if got[mount] != iface {
			t.Errorf("mount %s interface = %q, want %q", mount, got[mount], iface)
		}
	}
}
