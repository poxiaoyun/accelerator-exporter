package nodefacts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestResolveCPUModel(t *testing.T) {
	t.Parallel()
	nodeWithPredefinedModel := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		CPUModelPredefinedLabel: "predefined model",
	}}}
	tests := []struct {
		name      string
		node      *corev1.Node
		brandName string
		vendor    string
		expected  string
	}{
		{name: "brand name", node: nodeWithPredefinedModel, brandName: "Intel Xeon", vendor: "GenuineIntel", expected: "Intel Xeon"},
		{name: "predefined model", node: nodeWithPredefinedModel, vendor: "GenuineIntel", expected: "predefined model"},
		{name: "vendor", node: &corev1.Node{}, vendor: "GenuineIntel", expected: "GenuineIntel"},
		{name: "generic", node: &corev1.Node{}, expected: "Generic"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if actual := resolveCPUModel(test.node, test.brandName, test.vendor); actual != test.expected {
				t.Fatalf("resolveCPUModel() = %q, want %q", actual, test.expected)
			}
		})
	}
}

func TestEncodeCPUModelName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz": "Intel-Xeon-Gold-6330-CPU",
		"AMD EPYC 7B12 64-Core Processor":          "AMD-EPYC-7B12-64-Core-Processor",
		"  Kunpeng  920 / ARM  ":                   "Kunpeng-920-ARM",
	}
	for input, expected := range tests {
		actual := EncodeCPUModelName(input)
		if actual != expected {
			t.Errorf("EncodeCPUModelName(%q) = %q, want %q", input, actual, expected)
		}
		if problems := validation.IsValidLabelValue(actual); len(problems) > 0 {
			t.Errorf("EncodeCPUModelName(%q) is invalid: %v", input, problems)
		}
	}
	long := EncodeCPUModelName(strings.Repeat("CPU model ", 20))
	if len(long) == 0 || len(long) > 63 {
		t.Fatalf("long encoded model has invalid length %d: %q", len(long), long)
	}
}

func TestCPUModelPublisherPublish(t *testing.T) {
	t.Parallel()
	client := &fakeNodeClient{node: &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}}
	publisher := NewCPUModelPublisher(client, "node-a", slog.New(slog.NewTextHandler(io.Discard, nil)))
	publisher.detectCPU = func() (string, string) {
		return "Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz", "GenuineIntel"
	}

	if err := publisher.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.patchedName != "node-a" {
		t.Fatalf("patched node = %q, want node-a", client.patchedName)
	}
	if actual := client.node.Labels[CPUModelLabel]; actual != "Intel-Xeon-Gold-6330-CPU" {
		t.Fatalf("CPU model label = %q", actual)
	}
	if actual := client.node.Annotations[CPUModelRawAnnotation]; actual != "Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz" {
		t.Fatalf("raw CPU model annotation = %q", actual)
	}

	if err := publisher.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.patchCalls != 1 {
		t.Fatalf("patch calls = %d, want 1 for unchanged metadata", client.patchCalls)
	}
}

type fakeNodeClient struct {
	node        *corev1.Node
	patchedName string
	patchCalls  int
}

func (client *fakeNodeClient) Get(_ context.Context, name string, _ metav1.GetOptions) (*corev1.Node, error) {
	if client.node.Name != name {
		return nil, nil
	}
	return client.node.DeepCopy(), nil
}

func (client *fakeNodeClient) Patch(_ context.Context, name string, patchType types.PatchType, data []byte, _ metav1.PatchOptions, _ ...string) (*corev1.Node, error) {
	if patchType != types.MergePatchType {
		panic("unexpected patch type")
	}
	var patch struct {
		Metadata struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &patch); err != nil {
		return nil, err
	}
	if client.node.Labels == nil {
		client.node.Labels = map[string]string{}
	}
	if client.node.Annotations == nil {
		client.node.Annotations = map[string]string{}
	}
	for key, value := range patch.Metadata.Labels {
		client.node.Labels[key] = value
	}
	for key, value := range patch.Metadata.Annotations {
		client.node.Annotations[key] = value
	}
	client.patchedName = name
	client.patchCalls++
	return client.node.DeepCopy(), nil
}
