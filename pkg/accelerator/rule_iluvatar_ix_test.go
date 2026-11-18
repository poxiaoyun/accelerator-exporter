package accelerator

import "testing"

func iluvatarEnvironment() Environment {
	return Environment{Source: Source{Name: "ix-exporter/ty1200", Rule: RuleIluvatarIX, Labels: map[string]string{LabelNode: "ty1200"}}}
}

func iluvatarLabels() map[string]string {
	// Official 4.3.0 non-Kubernetes mode exposes these three labels only.
	return map[string]string{"gpu": "0", "name": "MRC-V100-0x1B", "uuid": "GPU-468a5c87-da50-4a5a-96d7-8debc120e9e2"}
}

func TestIluvatarIXUnitsAndIdentity(t *testing.T) {
	t.Parallel()
	raw := Families{}
	for name, value := range map[string]float64{
		"ix_gpu_utilization": 75, "ix_mem_total": 32768, "ix_mem_used": 8192,
		"ix_mem_free": 24500, "ix_mem_utilization": 91, "ix_temperature": 39, "ix_power_usage": 24,
	} {
		raw[name] = []RawSample{{Labels: iluvatarLabels(), Value: value}}
	}
	result := ruleFor(t, RuleIluvatarIX).Apply(iluvatarEnvironment(), raw)
	for name, value := range map[string]float64{
		"accelerator_core_utilization_percentage":             75,
		"accelerator_memory_total_bytes":                      34359738368,
		"accelerator_memory_used_bytes":                       8589934592,
		"accelerator_memory_free_bytes":                       24500 * bytesPerMiB,
		"accelerator_memory_utilization_percentage":           25,
		"accelerator_memory_bandwidth_utilization_percentage": 91,
		"accelerator_temperature_celsius":                     39, "accelerator_power_usage_watts": 24,
	} {
		sample := findSample(t, result.Samples, name)
		if sample.Value != value || sample.Labels[LabelNode] != "ty1200" ||
			sample.Labels[LabelDeviceID] != iluvatarLabels()["uuid"] || sample.Labels[LabelModel] != "mrc-v100" ||
			sample.Labels[LabelVendor] != VendorIluvatar || sample.Labels[LabelDeviceMode] != DeviceModePhysical {
			t.Errorf("%s = %#v, want %v on the physical MRC-V100", name, sample, value)
		}
	}
	for _, name := range []string{"accelerator_power_limit_watts", "accelerator_health_state", "accelerator_errors_total"} {
		if got := findSamples(t, result.Samples, name); len(got) != 0 {
			t.Errorf("unmeasured %s must stay absent: %#v", name, got)
		}
	}
	info := findSample(t, result.Samples, InventoryMetricName)
	if info.Labels[LabelDriverVersion] != "" || info.Labels[LabelDeviceType] != DeviceTypeGPU {
		t.Fatalf("4.3.0 inventory = %#v", info)
	}
}

func TestIluvatarIXAllocationUsesExactChipUUID(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleIluvatarIX).Apply(iluvatarEnvironment(), Families{
		"ix_gpu_utilization": {{Labels: iluvatarLabels(), Value: 0}},
	})
	allocations := []Allocation{
		{Namespace: "ns", Pod: "real", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.com/gpu", NativeDeviceID: iluvatarLabels()["uuid"]},
		{Namespace: "ns", Pod: "wrong-vendor", PodUID: "uid", Container: "worker", NativeResource: "nvidia.com/gpu", NativeDeviceID: iluvatarLabels()["uuid"]},
		{Namespace: "ns", Pod: "index", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.com/gpu", NativeDeviceID: "0"},
		{Namespace: "ns", Pod: "replica", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.com/gpu", NativeDeviceID: iluvatarLabels()["uuid"] + "::1"},
	}
	got := BuildPodInfo(allocations, result.AllocationIndex)
	if len(got) != 1 || got[0].Labels[LabelPod] != "real" || got[0].Labels[LabelDeviceID] != iluvatarLabels()["uuid"] {
		t.Fatalf("allocation relations = %#v", got)
	}
}

func TestIluvatarIXMissingUUIDDoesNotUseIndex(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleIluvatarIX).Apply(iluvatarEnvironment(), Families{
		"ix_gpu_utilization": {{Labels: withLabel(iluvatarLabels(), "uuid", ""), Value: 0}},
	})
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	if sample.Labels[LabelDeviceID] != "" || len(result.AllocationIndex) != 0 {
		t.Fatalf("missing UUID must remain an identity gap: %#v", result)
	}
}

func TestIluvatarIXMissingMeasurementsAndECCStatus(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleIluvatarIX).Apply(iluvatarEnvironment(), Families{
		"ix_mem_total":          {{Labels: iluvatarLabels(), Value: 32768}},
		"ix_ecc_sbe_vol_status": {{Labels: iluvatarLabels(), Value: 0}},
		"ix_ecc_dbe_vol_status": {{Labels: iluvatarLabels(), Value: 1}},
	})
	for _, name := range []string{"accelerator_memory_used_bytes", "accelerator_memory_free_bytes", "accelerator_memory_utilization_percentage", "accelerator_errors_total", "accelerator_health_state"} {
		if got := findSamples(t, result.Samples, name); len(got) != 0 {
			t.Errorf("missing measurement/boolean status must not fabricate %s: %#v", name, got)
		}
	}
	diagnostics := findSamples(t, result.Samples, "accelerator_health_diagnostic_info")
	if len(diagnostics) != 1 || diagnostics[0].Labels["severity"] != SeverityCritical || diagnostics[0].Value != 1 {
		t.Fatalf("ECC diagnostic = %#v", diagnostics)
	}
}

func TestIluvatarIXLocalNodeAndOptionalMetadata(t *testing.T) {
	t.Parallel()
	raw := withLabels(iluvatarLabels(), map[string]string{"node_name": "stale-hostname", "driver": "4.4.0", "serial": "serial-1"})
	result := ruleFor(t, RuleIluvatarIX).Apply(iluvatarEnvironment(), Families{
		"ix_gpu_utilization": {{Labels: raw, Value: 0}},
	})
	info := findSample(t, result.Samples, InventoryMetricName)
	if info.Labels[LabelNode] != "ty1200" || info.Labels[LabelDriverVersion] != "4.4.0" || info.Labels[LabelSerialNumber] != "serial-1" {
		t.Fatalf("node identity/metadata = %#v", info)
	}
}

func TestIluvatarIXDriverAliasKeepsOnePlatformModel(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleIluvatarIX).Apply(iluvatarEnvironment(), Families{
		"ix_gpu_utilization": {{Labels: iluvatarLabels(), Value: 50}},
		"ix_mem_total":       {{Labels: withLabel(iluvatarLabels(), "name", "MRC-V100"), Value: 32768}},
	})
	for _, sample := range result.Samples {
		if sample.Labels[LabelModel] != "mrc-v100" {
			t.Errorf("driver alias split the platform model: %#v", sample)
		}
	}
	if len(result.Identities) != 1 {
		t.Fatalf("model alias must keep one physical device: %#v", result.Identities)
	}
}
