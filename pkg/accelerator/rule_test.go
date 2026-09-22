package accelerator

import (
	"sort"
	"strings"
	"testing"
)

func TestRuleRegistry(t *testing.T) {
	t.Parallel()
	want := []string{RuleAscendNPU, RuleNvidiaDCGM, RuleVolcanoVGPU}
	if got := RuleNames(); len(got) != len(want) {
		t.Fatalf("RuleNames() = %v, want %v", got, want)
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("RuleNames() = %v, want %v", got, want)
			}
		}
	}
	for _, name := range want {
		if _, ok := RuleByName(name); !ok {
			t.Errorf("RuleByName(%q) not found", name)
		}
	}
	if _, ok := RuleByName("gpu"); ok {
		t.Error("a rule must be named after the source, not after the device class")
	}
}

// TestDeviceLayerNeverCarriesOwnershipOrVendorLabels is the regression net for
// the highest-cardinality defect the rework fixed: Pod identity and vendor
// labels used to ride on every device metric.
func TestDeviceLayerNeverCarriesOwnershipOrVendorLabels(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"namespace", "pod", "pod_uid", "container", "instance",
		"accelerator_type", "uuid", "partition", "quantile",
		"Hostname", "hostname", "UUID", "modelName", "model_name",
		"device", "id", "vdie_id", "deviceuuid", "vdeviceid", "v_dev_id",
		"GPU_I_ID", "gpu_instance_id", "podnamespace", "podname", "ctrname",
		"health_watch", "health_error_code", "health_error_severity", "health_error_category",
		"share_basis", "DCGM_FI_DRIVER_VERSION",
	}
	allowed := map[string]bool{
		LabelNode: true, LabelDeviceID: true, LabelParentDeviceID: true,
		LabelDeviceType: true, LabelDeviceMode: true, LabelDeviceIndex: true,
		LabelVendor: true, LabelModel: true,
		LabelProfile: true, LabelDriverVersion: true, LabelFirmwareVersion: true,
		LabelSerialNumber: true, LabelPCIBusID: true, LabelArchitecture: true,
		"state": true, "category": true, "code": true, "severity": true,
		"reason": true, "error_type": true, "error_code": true,
	}
	for _, fixture := range fixtures {
		result := applyFixture(t, fixture)
		for _, sample := range result.Samples {
			metadata, ok := CanonicalMetric(sample.Name)
			if !ok {
				t.Fatalf("fixture %q produced %q, which is not in the contract", fixture.name, sample.Name)
			}
			if metadata.Layer != LayerDevice {
				continue
			}
			for _, name := range forbidden {
				if _, present := sample.Labels[name]; present {
					t.Errorf("fixture %q leaked %q onto %s", fixture.name, name, sample.Name)
				}
			}
			for name := range sample.Labels {
				if !allowed[name] {
					t.Errorf("fixture %q put the undeclared label %q on %s", fixture.name, name, sample.Name)
				}
			}
			if metadata.Modes != nil && !contains(metadata.Modes, sample.Labels[LabelDeviceMode]) {
				t.Errorf("fixture %q put %s on a %s target", fixture.name, sample.Name, sample.Labels[LabelDeviceMode])
			}
		}
	}
}

func TestApplyReportsCanonicalPassthroughWithResolvedIdentity(t *testing.T) {
	t.Parallel()
	source := Source{Name: "custom", Rule: RuleNvidiaDCGM}
	samples := convert(t, ruleFor(t, RuleNvidiaDCGM).Apply(Environment{Source: source},
		Families{"accelerator_io_utilization_percentage": {{Labels: dcgmLabels(), Value: 12}}}))

	sample := findSample(t, samples, "accelerator_io_utilization_percentage")
	if sample.Value != 12 || sample.Labels[LabelDeviceID] != "GPU-1" || sample.Labels[LabelDeviceType] != DeviceTypeGPU {
		t.Fatalf("unexpected passthrough sample: %#v", sample)
	}
	// The inventory metric is derived from the resolved identity, so it carries
	// the static information the passthrough sample does not.
	info := findSample(t, samples, InventoryMetricName)
	if info.Labels[LabelPCIBusID] != "00000000:07:00.0" || info.Labels[LabelDriverVersion] != "550.90.07" {
		t.Fatalf("inventory did not receive the static information: %#v", info)
	}
}

func TestApplyReportsUnidentifiedMeasurements(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleNvidiaDCGM).Apply(Environment{Source: Source{Name: "dcgm", Rule: RuleNvidiaDCGM}},
		Families{"DCGM_FI_DEV_GPU_UTIL": {{Labels: map[string]string{"Hostname": "node-a"}, Value: 42}}})
	if len(result.Samples) == 0 {
		t.Fatal("a measurement without a device_id must still be reported")
	}
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	if sample.Value != 42 || sample.Labels[LabelModel] != ModelUnknown {
		t.Fatalf("unexpected sample: %#v", sample)
	}
	if _, present := sample.Labels[LabelDeviceID]; present {
		t.Errorf("an unresolved device_id must be omitted, not invented: %#v", sample)
	}
}

func convert(t *testing.T, result Result) []Sample {
	t.Helper()
	return result.Samples
}

func findSample(t *testing.T, samples []Sample, name string) Sample {
	t.Helper()
	for _, sample := range samples {
		if sample.Name == name {
			return sample
		}
	}
	t.Fatalf("sample %s not found in %s", name, renderSamples(samples))
	return Sample{}
}

func findSamples(t *testing.T, samples []Sample, name string) []Sample {
	t.Helper()
	var found []Sample
	for _, sample := range samples {
		if sample.Name == name {
			found = append(found, sample)
		}
	}
	return found
}

func findSampleWithLabel(t *testing.T, samples []Sample, name, label, value string) Sample {
	t.Helper()
	for _, sample := range samples {
		if sample.Name == name && sample.Labels[label] == value {
			return sample
		}
	}
	t.Fatalf("sample %s{%s=%q} not found in %s", name, label, value, renderSamples(samples))
	return Sample{}
}

func renderSamples(samples []Sample) string {
	lines := make([]string, 0, len(samples))
	for _, sample := range samples {
		lines = append(lines, sample.Name+formatLabels(sample.Labels))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func formatLabels(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+labels[name])
	}
	return "{" + strings.Join(pairs, ",") + "}"
}
