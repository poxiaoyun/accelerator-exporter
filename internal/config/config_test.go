package config

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeDynamicSourcesAndDefaults(t *testing.T) {
	t.Parallel()
	runtime, err := Decode(strings.NewReader(`
sources:
  - name: gpu-pods
    acceleratorType: GPU
    podMonitor:
      namespaceSelector:
        matchNames: [gpu-operator]
      selector:
        matchLabels:
          app: dcgm
      port: metrics
  - name: npu-services
    acceleratorType: npu
    labels:
      vendor_stack: ascend
    serviceMonitor:
      namespaceSelector:
        matchNames: [npu-exporter]
      selector:
        matchLabels:
          app: npu-exporter
      port: http-metrics
`))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ListenAddress != ":9400" || runtime.ScrapeInterval != 15*time.Second || runtime.ScrapeTimeout != 5*time.Second || runtime.MaxConcurrency != 8 {
		t.Fatalf("unexpected defaults: %#v", runtime)
	}
	if len(runtime.PodMonitors) != 1 || runtime.PodMonitors[0].AcceleratorType != "gpu" {
		t.Fatalf("unexpected Pod monitors: %#v", runtime.PodMonitors)
	}
	if len(runtime.ServiceMonitors) != 1 || runtime.ServiceMonitors[0].Labels["vendor_stack"] != "ascend" {
		t.Fatalf("unexpected Service monitors: %#v", runtime.ServiceMonitors)
	}
}

func TestDecodeRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	tests := []string{
		`sources: []`,
		`sources: [{name: a, address: http://host/metrics, acceleratorType: gpu}]`,
		`sources: [{name: a, acceleratorType: tpu, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		`sources: [{name: a, acceleratorType: gpu, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}, {name: a, acceleratorType: npu, serviceMonitor: {namespaces: [npu], selector: {matchLabels: {app: npu}}, port: metrics}}]`,
		`sources: [{name: a, acceleratorType: gpu, labels: {accelerator_type: npu}, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		`sources: [{name: a, acceleratorType: gpu, podMonitor: {namespaces: [gpu], selector: {}, port: metrics}}]`,
		`sources: [{name: a, acceleratorType: gpu, podMonitor: {namespaceSelector: {any: true, matchNames: [gpu]}, selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		`sources: [{name: a, acceleratorType: gpu, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}, serviceMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		`unknownField: true
sources: [{name: a, acceleratorType: gpu, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
	}
	for _, input := range tests {
		if _, err := Decode(strings.NewReader(input)); err == nil {
			t.Errorf("expected error for config: %s", input)
		}
	}
}

func TestDecodePodAndServiceMonitors(t *testing.T) {
	t.Parallel()
	runtime, err := Decode(strings.NewReader(`
kubeconfig: /tmp/kubeconfig
sources:
  - name: dcgm
    acceleratorType: gpu
    podMonitor:
      namespaceSelector:
        matchNames: [gpu-operator]
      selector:
        matchLabels:
          app: dcgm
        matchExpressions:
          - key: metrics
            operator: In
            values: [enabled]
      port: metrics
  - name: npu
    acceleratorType: npu
    serviceMonitor:
      namespaceSelector:
        matchNames: [npu-exporter]
      selector:
        matchLabels:
          app: npu-exporter
      port: http
      path: /custom-metrics
  - name: vgpu
    acceleratorType: vgpu
    podMonitor:
      namespaceSelector:
        any: true
      selector:
        matchLabels:
          app: vgpu
      port: 9394
`))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Kubeconfig != "/tmp/kubeconfig" || len(runtime.PodMonitors) != 2 || len(runtime.ServiceMonitors) != 1 {
		t.Fatalf("unexpected runtime: %#v", runtime)
	}
	if runtime.PodMonitors[0].Selector.MatchLabels["app"] != "dcgm" || runtime.PodMonitors[0].Path != "/metrics" {
		t.Fatalf("unexpected Pod monitor: %#v", runtime.PodMonitors[0])
	}
	if runtime.ServiceMonitors[0].Port != "http" || runtime.ServiceMonitors[0].Path != "/custom-metrics" {
		t.Fatalf("unexpected Service monitor: %#v", runtime.ServiceMonitors[0])
	}
	if runtime.PodMonitors[1].Port != "9394" {
		t.Fatalf("numeric port = %q", runtime.PodMonitors[1].Port)
	}
	if !runtime.PodMonitors[1].AnyNamespace {
		t.Fatal("namespaceSelector.any was not preserved")
	}
}

func TestExampleConfig(t *testing.T) {
	t.Parallel()
	runtime, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.PodMonitors) != 1 || len(runtime.ServiceMonitors) != 3 {
		t.Fatalf("unexpected example source counts: pod=%d service=%d", len(runtime.PodMonitors), len(runtime.ServiceMonitors))
	}
}
