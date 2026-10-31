package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadOptionsAppliesDefaults(t *testing.T) {
	t.Parallel()
	options, err := LoadOptions(writeConfig(t, `
sources:
  - name: gpu-pods
    rule: nvidia-dcgm
    podMonitor:
      namespaceSelector:
        matchNames: [gpu-operator]
      selector:
        matchLabels:
          app: dcgm
      port: metrics
`))
	if err != nil {
		t.Fatal(err)
	}
	if options.ListenAddress != ":9400" ||
		options.ScrapeInterval != 15*time.Second ||
		options.ScrapeTimeout != 5*time.Second ||
		options.MaxConcurrency != 8 ||
		options.PodResourcesSocket != DefaultPodResourcesSocket {
		t.Fatalf("defaults = %#v", options)
	}
	if len(options.Sources) != 1 || options.Sources[0].Rule != "nvidia-dcgm" {
		t.Fatalf("sources = %#v", options.Sources)
	}
}

func TestLoadOptionsRejectsInvalidConfigurations(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"no monitor kind":     `sources: [{name: a, rule: nvidia-dcgm}]`,
		"both monitor kinds":  `sources: [{name: a, rule: nvidia-dcgm, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}, serviceMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		"unknown rule":        `sources: [{name: a, rule: tpu, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		"duplicate names":     `sources: [{name: a, rule: nvidia-dcgm, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}, {name: a, rule: ascend-npu, serviceMonitor: {namespaces: [npu], selector: {matchLabels: {app: npu}}, port: metrics}}]`,
		"pinned identity":     `sources: [{name: a, rule: nvidia-dcgm, labels: {device_id: pinned}, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		"empty selector":      `sources: [{name: a, rule: nvidia-dcgm, podMonitor: {namespaces: [gpu], selector: {}, port: metrics}}]`,
		"any plus matchNames": `sources: [{name: a, rule: nvidia-dcgm, podMonitor: {namespaceSelector: {any: true, matchNames: [gpu]}, selector: {matchLabels: {app: dcgm}}, port: metrics}}]`,
		"unknown field":       "unknownField: true\nsources: [{name: a, rule: nvidia-dcgm, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]",
		"zero interval":       "scrapeInterval: 0s\nsources: [{name: a, rule: nvidia-dcgm, podMonitor: {namespaces: [gpu], selector: {matchLabels: {app: dcgm}}, port: metrics}}]",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadOptions(writeConfig(t, contents)); err == nil {
				t.Errorf("expected an error for:\n%s", contents)
			}
		})
	}
}

func TestLoadOptionsAllowsEmptySourcesForDiskDiscovery(t *testing.T) {
	t.Parallel()
	options, err := LoadOptions(writeConfig(t, `sources: []`))
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Sources) != 0 {
		t.Fatalf("sources = %#v, want empty", options.Sources)
	}
}

func TestLoadOptionsRequiresAConfigurationFile(t *testing.T) {
	t.Parallel()
	if _, err := LoadOptions(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("LoadOptions(\"\") = %v", err)
	}
	if _, err := LoadOptions(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected a read error for a missing file")
	}
}

// TestExampleConfigStaysLoadable keeps config.example.yaml, the chart presets and
// the loader from drifting apart.
func TestExampleConfigStaysLoadable(t *testing.T) {
	t.Parallel()
	options, err := LoadOptions(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Sources) != 4 {
		t.Fatalf("example has %d sources, want 4", len(options.Sources))
	}
	for _, source := range options.Sources {
		if (source.PodMonitor == nil) == (source.ServiceMonitor == nil) {
			t.Errorf("source %q must use exactly one monitor kind", source.Name)
		}
	}
}
