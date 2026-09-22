package scrapetargets

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

// podSource builds the smallest valid Pod-based source, so each case only has to
// state what it is testing.
func podSource(mutate func(*Source)) Source {
	source := Source{
		Name: "dcgm",
		Rule: accelerator.RuleNvidiaDCGM,
		PodMonitor: &MonitorSpec{
			Namespaces: []string{"rune-system"},
			Selector:   LabelSelector{MatchLabels: map[string]string{"app": "dcgm"}},
			Port:       "metrics",
		},
	}
	mutate(&source)
	return source
}

func TestSourceValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		source  Source
		wantErr string
	}{
		{name: "minimal pod source", source: podSource(func(*Source) {})},
		{
			name: "service source",
			source: Source{Name: "npu", Rule: accelerator.RuleAscendNPU, ServiceMonitor: &MonitorSpec{
				Namespaces: []string{"npu-exporter"},
				Selector:   LabelSelector{MatchLabels: map[string]string{"app": "npu-exporter"}},
				Port:       "http-metrics",
			}},
		},
		{
			name:    "missing name",
			source:  podSource(func(source *Source) { source.Name = "  " }),
			wantErr: "source name is required",
		},
		{
			name:    "unknown rule",
			source:  podSource(func(source *Source) { source.Rule = "tpu" }),
			wantErr: "unknown rule",
		},
		{
			name:    "no monitor",
			source:  podSource(func(source *Source) { source.PodMonitor = nil }),
			wantErr: "exactly one of podMonitor or serviceMonitor",
		},
		{
			name:    "both monitors",
			source:  podSource(func(source *Source) { source.ServiceMonitor = source.PodMonitor }),
			wantErr: "exactly one of podMonitor or serviceMonitor",
		},
		{
			name: "no namespaces",
			source: podSource(func(source *Source) {
				source.PodMonitor.Namespaces = nil
			}),
			wantErr: "select at least one namespace",
		},
		{
			name: "any together with matchNames",
			source: podSource(func(source *Source) {
				source.PodMonitor.NamespaceSelector = NamespaceSelector{Any: true, MatchNames: []string{"gpu"}}
			}),
			wantErr: "cannot be combined",
		},
		{
			name:    "missing port",
			source:  podSource(func(source *Source) { source.PodMonitor.Port = "" }),
			wantErr: "port is required",
		},
		{
			name:    "numeric port out of range",
			source:  podSource(func(source *Source) { source.PodMonitor.Port = "70000" }),
			wantErr: "between 1 and 65535",
		},
		{
			name:    "invalid port name",
			source:  podSource(func(source *Source) { source.PodMonitor.Port = "Not_A_Port" }),
			wantErr: "port name is invalid",
		},
		{
			name:    "path without a leading slash",
			source:  podSource(func(source *Source) { source.PodMonitor.Path = "metrics" }),
			wantErr: "path must start with /",
		},
		{
			name:    "unknown scheme",
			source:  podSource(func(source *Source) { source.PodMonitor.Scheme = "ftp" }),
			wantErr: "scheme must be http or https",
		},
		{
			name:    "empty selector",
			source:  podSource(func(source *Source) { source.PodMonitor.Selector = LabelSelector{} }),
			wantErr: "selector must not be empty",
		},
		{
			name:    "invalid label name",
			source:  podSource(func(source *Source) { source.Labels = map[string]string{"bad-label": "x"} }),
			wantErr: "invalid label name",
		},
		{
			name:    "identity label pinned from configuration",
			source:  podSource(func(source *Source) { source.Labels = map[string]string{accelerator.LabelDeviceID: "GPU-1"} }),
			wantErr: "must not set label",
		},
		{
			name:    "node pinned from configuration",
			source:  podSource(func(source *Source) { source.Labels = map[string]string{accelerator.LabelNode: "node-a"} }),
			wantErr: "must not set label",
		},
		{
			name:   "model is the one pinnable label",
			source: podSource(func(source *Source) { source.Labels = map[string]string{accelerator.LabelModel: "A100"} }),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.source.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want no error", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestSourceResolveAppliesDefaults(t *testing.T) {
	t.Parallel()
	source := podSource(func(source *Source) {
		source.PodMonitor.NamespaceSelector = NamespaceSelector{MatchNames: []string{"other"}}
	})
	resolved := source.resolve(*source.PodMonitor)

	if resolved.path != "/metrics" || resolved.scheme != "http" || resolved.port != "metrics" {
		t.Fatalf("defaults not applied: %#v", resolved)
	}
	if resolved.name != "dcgm" || resolved.rule != accelerator.RuleNvidiaDCGM {
		t.Fatalf("source identity was not carried over: %#v", resolved)
	}
	// Both ways of naming namespaces are merged and de-duplicated.
	if len(resolved.namespaces) != 2 || resolved.namespaces[0] != "rune-system" || resolved.namespaces[1] != "other" {
		t.Fatalf("namespaces = %#v", resolved.namespaces)
	}
}

func TestPortAcceptsANameOrANumber(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]Port{"metrics": "metrics", "9394": "9394", `"9394"`: "9394"} {
		var decoded struct {
			Port Port `yaml:"port"`
		}
		if err := yaml.Unmarshal([]byte("port: "+input+"\n"), &decoded); err != nil {
			t.Fatalf("port %s: %v", input, err)
		}
		if decoded.Port != want {
			t.Errorf("port %s decoded to %q, want %q", input, decoded.Port, want)
		}
	}
	var decoded struct {
		Port Port `yaml:"port"`
	}
	if err := yaml.Unmarshal([]byte("port: [a]\n"), &decoded); err == nil {
		t.Error("a sequence is not a port")
	}
}

func TestProvidersIgnoreSourcesOfTheOtherKind(t *testing.T) {
	t.Parallel()
	sources := []Source{
		podSource(func(*Source) {}),
		{Name: "npu", Rule: accelerator.RuleAscendNPU, ServiceMonitor: &MonitorSpec{
			Namespaces: []string{"npu"},
			Selector:   LabelSelector{MatchLabels: map[string]string{"app": "npu"}},
			Port:       "http",
		}},
	}
	podProvider, err := NewPodProvider(&fakePodLister{}, "node-a", sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(podProvider.monitors) != 1 || podProvider.monitors[0].name != "dcgm" {
		t.Fatalf("Pod provider monitors = %#v", podProvider.monitors)
	}
	serviceProvider, err := NewServiceProvider(&fakeServiceEndpointLister{}, "node-a", sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(serviceProvider.monitors) != 1 || serviceProvider.monitors[0].name != "npu" {
		t.Fatalf("Service provider monitors = %#v", serviceProvider.monitors)
	}
}

func TestProvidersRejectAnInvalidSource(t *testing.T) {
	t.Parallel()
	if _, err := NewPodProvider(&fakePodLister{}, "node-a", []Source{podSource(func(source *Source) {
		source.Rule = "tpu"
	})}); err == nil {
		t.Fatal("a provider must not accept an invalid source")
	}
}
