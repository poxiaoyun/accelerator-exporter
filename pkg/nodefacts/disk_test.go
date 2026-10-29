package nodefacts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	publisher := NewDiskPublisher(client, "node-a", fakeDiskDetector{disks: []Disk{{ID: "wwn-test", MountPath: "/data"}}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := publisher.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	var disks []Disk
	if err := json.Unmarshal([]byte(client.node.Annotations[IdleDisksAnnotation]), &disks); err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 || disks[0].ID != "wwn-test" || disks[0].MountPath != "/data" {
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
