package exporter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// Exercise native HTTP parsing, rule selection, units, deduplication and kubelet
// allocation joins together. Schema follows official IX 4.3.0, values are
// synthetic; this is not a captured hardware scrape.
func TestIluvatarRawEndpointProducesDeviceAndAllocationLayers(t *testing.T) {
	t.Parallel()
	const raw = `ix_gpu_utilization{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc",pod="untrusted"} 75
ix_mem_total{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc"} 32768
ix_mem_used{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc",container="a"} 8192
ix_mem_used{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc",container="b"} 8192
ix_mem_free{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc"} 24500
ix_mem_utilization{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc"} 91
ix_temperature{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc"} 39
ix_power_usage{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc"} 24
`
	scraper := NewHTTPScraper(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
	})})
	targets := fakeTargetProvider{targets: []accelerator.Source{{Name: "ix-exporter/ty1200", Address: "http://ix/metrics", Rule: accelerator.RuleIluvatarIX, Labels: map[string]string{accelerator.LabelNode: "ty1200"}}}}
	allocations := fakeAllocationProvider{allocations: []accelerator.Allocation{
		{Namespace: "ns", Pod: "real", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.com/gpu", NativeDeviceID: "GPU-mrc"},
		{Namespace: "ns", Pod: "wrong-resource", PodUID: "uid", Container: "worker", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-mrc"},
		{Namespace: "ns", Pod: "numeric-id", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.com/gpu", NativeDeviceID: "0"},
	}}
	exporter, err := New(NewDefaultOptions(), scraper, targets, allocations, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(exporter)
	for _, want := range []string{
		`accelerator_memory_utilization_percentage{device_id="GPU-mrc",device_mode="physical",device_type="gpu",model="mrc-v100",node="ty1200",vendor="iluvatar"} 25`,
		`accelerator_memory_used_bytes{device_id="GPU-mrc",device_mode="physical",device_type="gpu",model="mrc-v100",node="ty1200",vendor="iluvatar"} 8.589934592e+09`,
		`accelerator_pod_info{container="worker",device_id="GPU-mrc",namespace="ns",node="ty1200",pod="real",pod_uid="uid"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s:\n%s", want, body)
		}
	}
	if strings.Count(body, "\naccelerator_pod_info{") != 1 || strings.Count(body, "\naccelerator_memory_used_bytes{") != 1 {
		t.Errorf("unexpected device/allocation duplication:\n%s", body)
	}
	if strings.Contains(body, "untrusted") || strings.Contains(body, `gpu="0"`) || strings.Contains(body, `uuid=`) || strings.Contains(body, "wrong-resource") || strings.Contains(body, "numeric-id") {
		t.Errorf("native labels or invalid ownership leaked:\n%s", body)
	}
}

func TestIluvatarVirtualEndpointJoinsCurrentPhysicalScrape(t *testing.T) {
	const physical = `ix_gpu_utilization{gpu="0",name="MRC-V100-0x1B",uuid="GPU-mrc"} 75
`
	const virtual = `container_gpu_utilization{pod_name="real",namespace="ns",node="ty1200",container_name="worker",modelName="MRC-V100-0x1B",gpu="gpu0"} 25
container_gpu_utilization{pod_name="real",namespace="ns",node="ty1200",container_name="worker",modelName="MRC-V100-0x1B",gpu="total"} 25
container_gpu_memory_total{pod_name="real",namespace="ns",node="ty1200",container_name="worker",modelName="MRC-V100-0x1B",gpu_memory="gpu0"} 1024
container_gpu_memory_total{pod_name="real",namespace="ns",node="ty1200",container_name="worker",modelName="MRC-V100-0x1B",gpu_memory="total"} 1024
`
	scraper := NewHTTPScraper(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := physical
		if request.URL.Host == "manager" {
			body = virtual
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	targets := fakeTargetProvider{targets: []accelerator.Source{
		{Name: "ix", Address: "http://ix/metrics", Rule: accelerator.RuleIluvatarIX, Labels: map[string]string{accelerator.LabelNode: "ty1200"}},
		{Name: "manager", Address: "http://manager/metric", Rule: accelerator.RuleIluvatarVGPU, Labels: map[string]string{accelerator.LabelNode: "ty1200"}},
	}}
	allocations := fakeAllocationProvider{allocations: []accelerator.Allocation{
		{Namespace: "ns", Pod: "real", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.ai/MRC-V100.vCore", NativeDeviceID: "iluvatar.ai/MRC-V100.vCore-0"},
		{Namespace: "ns", Pod: "real", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.ai/MRC-V100.vMem", NativeDeviceID: "iluvatar.ai/MRC-V100.vMem-268435456-0"},
	}}
	exporter, err := New(NewDefaultOptions(), scraper, targets, allocations, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := metricsBody(exporter)
	if strings.Count(body, "\naccelerator_core_utilization_percentage{") != 2 || strings.Count(body, "\naccelerator_memory_used_bytes{") != 1 || strings.Count(body, "\naccelerator_pod_info{") != 1 {
		t.Fatalf("duplicated or missing observations:\n%s", body)
	}
	if !strings.Contains(body, `device_mode="virtual",device_type="gpu",model="mrc-v100",node="ty1200",parent_device_id="GPU-mrc",vendor="iluvatar"} 1.073741824e+09`) {
		t.Fatalf("virtual memory or parent missing:\n%s", body)
	}
	if strings.Contains(body, "\naccelerator_memory_total_bytes{") || strings.Contains(body, `gpu_memory=`) {
		t.Fatalf("aggregate capacity or vendor labels leaked:\n%s", body)
	}
}
