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
