package accelerator

import (
	"testing"
)

// TestRuleMappingsProduceDistinctSeries encodes the specification rule that one
// metric name and one observation target can only come from one mapping. Two
// mappings that produce the same name with the same enum labels would silently
// overwrite each other, which is how the duplicate NPU power mapping used to
// behave.
func TestRuleMappingsProduceDistinctSeries(t *testing.T) {
	t.Parallel()
	for _, rule := range rules {
		for index, target := range rule.Targets {
			seen := map[string]string{}
			for _, mapping := range target.Metrics {
				key := mapping.Canonical + "\xff" + labelsKey(mapping.Enums)
				if previous, exists := seen[key]; exists {
					t.Errorf(
						"rule %s target %d maps both %s and %s onto %s with the same enum labels",
						rule.Name, index, previous, mapping.Source, mapping.Canonical,
					)
				}
				seen[key] = mapping.Source
			}
		}
	}
}

func TestNVIDIADCGMComposesPartitionIdentity(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[1])
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	if sample.Labels[LabelDeviceID] != "GPU-1.3" ||
		sample.Labels[LabelParentDeviceID] != "GPU-1" ||
		sample.Labels[LabelDeviceIndex] != "3" ||
		sample.Labels[LabelDeviceMode] != DeviceModePartition ||
		sample.Labels[LabelDeviceType] != DeviceTypeGPU {
		t.Fatalf("unexpected partition identity: %#v", sample.Labels)
	}
	// The profile is static information, so only the inventory carries it.
	info := findSample(t, result.Samples, InventoryMetricName)
	if info.Labels[LabelProfile] != "1g.10gb" {
		t.Fatalf("partition inventory lost the profile: %#v", info.Labels)
	}
	if _, present := sample.Labels[LabelProfile]; present {
		t.Errorf("profile must not appear on a measurement: %#v", sample.Labels)
	}
}

// TestNVIDIADCGMNeverCopiesPhysicalReadingsOntoAPartition covers the
// specification rule that a partition has no independent temperature or power.
func TestNVIDIADCGMNeverCopiesPhysicalReadingsOntoAPartition(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[1])
	for _, name := range []string{
		"accelerator_temperature_celsius",
		"accelerator_power_usage_watts",
		"accelerator_power_limit_watts",
		"accelerator_throttle_reason_info",
	} {
		for _, sample := range findSamples(t, result.Samples, name) {
			t.Errorf("%s must not be produced for a partition: %#v", name, sample)
		}
	}
}

func TestNVIDIADCGMHealthSeverityFanIn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		labels   map[string]string
		value    float64
		want     string
		severity string
	}{
		{name: "isolate is a fault", labels: map[string]string{"health_error_severity": "ISOLATE"}, value: 20, want: HealthStateFault, severity: SeverityCritical},
		{name: "warning degrades", labels: map[string]string{"health_error_severity": "WARNING"}, value: 1, want: HealthStateDegraded, severity: SeverityWarning},
		{name: "informational stays healthy", labels: map[string]string{"health_error_severity": "INFO"}, value: 1, want: HealthStateHealthy, severity: SeverityInfo},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			health := withLabels(dcgmLabels(), test.labels)
			health["health_error_code"] = "DCGM_FR_TEST"
			health["health_error_category"] = "HARDWARE_MEMORY"
			result := ruleFor(t, RuleNvidiaDCGM).Apply(Source{Name: "dcgm", Rule: RuleNvidiaDCGM}, Families{
				"DCGM_EXP_GPU_HEALTH_STATUS": {{Labels: health, Value: test.value}},
			}, nil)
			state := findSample(t, result.Samples, "accelerator_health_state")
			if state.Labels["state"] != test.want || state.Value != 1 {
				t.Fatalf("state = %#v, want %s", state, test.want)
			}
			diagnostic := findSample(t, result.Samples, "accelerator_health_diagnostic_info")
			if diagnostic.Value != 1 || diagnostic.Labels["severity"] != test.severity {
				t.Fatalf("diagnostic = %#v, want severity %s and value 1", diagnostic, test.severity)
			}
			if diagnostic.Labels["category"] != "hardware_memory" || diagnostic.Labels["code"] != "dcgm_fr_test" {
				t.Fatalf("diagnostic labels must be lower-cased: %#v", diagnostic.Labels)
			}
		})
	}
}

// TestNVIDIADCGMProducesNoHealthStateWithoutAHealthSource separates "healthy"
// from "not reported": a device whose health collectors are disabled must not
// look healthy.
func TestNVIDIADCGMProducesNoHealthStateWithoutAHealthSource(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[0])
	if len(findSamples(t, result.Samples, "accelerator_health_state")) == 0 {
		t.Fatal("a health source was present, so a state was expected")
	}
	quiet := ruleFor(t, RuleNvidiaDCGM).Apply(Source{Name: "dcgm", Rule: RuleNvidiaDCGM}, Families{
		"DCGM_FI_DEV_GPU_UTIL": {{Labels: dcgmLabels(), Value: 10}},
	}, nil)
	if samples := findSamples(t, quiet.Samples, "accelerator_health_state"); len(samples) != 0 {
		t.Fatalf("no health source must produce no state, got %#v", samples)
	}
}

func TestNVIDIADCGMErrorsCarryTheXIDCode(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[0])
	xid := findSampleWithLabel(t, result.Samples, "accelerator_errors_total", "error_type", "xid")
	if xid.Value != 3 || xid.Labels["error_code"] != "31" {
		t.Fatalf("unexpected XID error sample: %#v", xid)
	}
}

func TestBuildInventoryExtractsPhysicalDeviceIdentity(t *testing.T) {
	t.Parallel()
	inventory := BuildInventory([]InventoryInput{{
		Source:   Source{Name: "dcgm", Rule: RuleNvidiaDCGM},
		Families: Families{"DCGM_FI_DEV_FB_USED": {{Labels: dcgmLabels(), Value: 20480}}},
	}})
	if info := inventory["GPU-1"]; info.Node != "node-gpu" || info.Model != "NVIDIA A100-SXM4-80GB" {
		t.Fatalf("unexpected inventory: %#v", info)
	}
	// A source that is not a DCGM source contributes no physical device identity.
	if len(BuildInventory([]InventoryInput{{
		Source:   Source{Name: "npu", Rule: RuleAscendNPU},
		Families: Families{"npu_chip_info_hbm_total_memory": {{Labels: npuLabels(), Value: 65536}}},
	}})) != 0 {
		t.Fatal("only the DCGM source declares physical GPU identity")
	}
}

// TestNVIDIADCGMTotalIsTheDeviceCapacity covers why the total is a sum and not a
// derivation: FB_USED + FB_FREE alone leave out FB_RESERVED, which on an H100 is
// 1638 MiB, so the derived total would understate the card by about 2%.
func TestNVIDIADCGMTotalIsTheDeviceCapacity(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[0])
	total := findSample(t, result.Samples, "accelerator_memory_total_bytes")
	used := findSample(t, result.Samples, "accelerator_memory_used_bytes")
	free := findSample(t, result.Samples, "accelerator_memory_free_bytes")

	if total.Value != 81920*bytesPerMiB {
		t.Fatalf("total = %v, want FB_USED + FB_FREE + FB_RESERVED", total.Value)
	}
	if used.Value+free.Value >= total.Value {
		t.Fatalf(
			"used + free = %v must be below the capacity %v: the reserved part belongs to neither",
			used.Value+free.Value, total.Value,
		)
	}
	if utilization := findSample(t, result.Samples, "accelerator_memory_utilization_percentage"); utilization.Value != used.Value/total.Value*100 {
		t.Fatalf("utilization = %v, want used / total", utilization.Value)
	}
}

// TestNVIDIADCGMFallsBackToDerivedTotalWithoutReserved covers a source that does
// not report the reserved part: the sum cannot be formed, so the total falls back
// to the derived one, which the specification allows as long as it stays stable
// per observation target.
func TestNVIDIADCGMFallsBackToDerivedTotalWithoutReserved(t *testing.T) {
	t.Parallel()
	labels := dcgmLabels()
	result := ruleFor(t, RuleNvidiaDCGM).Apply(Source{Name: "dcgm", Rule: RuleNvidiaDCGM}, Families{
		"DCGM_FI_DEV_FB_USED": {{Labels: labels, Value: 1024}},
		"DCGM_FI_DEV_FB_FREE": {{Labels: labels, Value: 9216}},
	}, nil)
	if total := findSample(t, result.Samples, "accelerator_memory_total_bytes"); total.Value != 10240*bytesPerMiB {
		t.Fatalf("total = %v, want the derived used + free", total.Value)
	}
}
