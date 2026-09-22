package accelerator

import "testing"

func TestVolcanoVGPUVirtualIdentityAndParent(t *testing.T) {
	t.Parallel()
	labelNames := []string{LabelDeviceID, LabelDeviceMode, LabelDeviceType, LabelParentDeviceID, LabelModel, LabelNode}
	result := applyFixture(t, fixtures[4])
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	want := map[string]string{
		LabelDeviceID:       "slice-1",
		LabelDeviceMode:     DeviceModeVirtual,
		LabelDeviceType:     DeviceTypeGPU,
		LabelParentDeviceID: "GPU-2",
		LabelModel:          "L40",
		LabelNode:           "node-gpu",
	}
	for _, name := range labelNames {
		if sample.Labels[name] != want[name] {
			t.Errorf("%s = %q, want %q", name, sample.Labels[name], want[name])
		}
	}
}

// TestVolcanoVGPUInheritsHostMetadata covers why the inventory exists: the vGPU
// families omit node and model, so they come from the physical device that DCGM
// reported for the same identifier.
func TestVolcanoVGPUInheritsHostMetadata(t *testing.T) {
	t.Parallel()
	bare := map[string]string{"deviceuuid": "GPU-2", "vdeviceid": "slice-1"}
	inventory := Inventory{"GPU-2": {Node: "node-gpu", Model: "L40"}}
	result := ruleFor(t, RuleVolcanoVGPU).Apply(Source{Name: "vgpu", Rule: RuleVolcanoVGPU}, Families{
		"vGPU_device_memory_usage_in_bytes": {{Labels: bare, Value: 1}},
	}, inventory)
	sample := findSample(t, result.Samples, "accelerator_memory_used_bytes")
	if sample.Labels[LabelNode] != "node-gpu" || sample.Labels[LabelModel] != "L40" {
		t.Fatalf("the host metadata was not inherited: %#v", sample.Labels)
	}
}

// TestVolcanoVGPUReportsNoPhysicalReading covers the contract restriction: a
// slice has no independent temperature or power of its own.
func TestVolcanoVGPUReportsNoPhysicalReading(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	for _, name := range []string{
		"accelerator_temperature_celsius",
		"accelerator_power_usage_watts",
		"accelerator_errors_total",
		"accelerator_throttle_reason_info",
		"accelerator_health_state",
	} {
		if samples := findSamples(t, result.Samples, name); len(samples) != 0 {
			t.Errorf("a vGPU slice must not report %s: %#v", name, samples)
		}
	}
}

// TestVolcanoVGPUMemoryTotalIsALimit: for a virtual instance the contract's
// memory total is the limit, and limits of several slices must never be summed.
func TestVolcanoVGPUMemoryTotalIsALimit(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	total := findSample(t, result.Samples, "accelerator_memory_total_bytes")
	if total.Value != 8589934592 {
		t.Fatalf("memory total = %v, want the slice limit, not a capacity", total.Value)
	}
	used := findSample(t, result.Samples, "accelerator_memory_used_bytes")
	utilization := findSample(t, result.Samples, "accelerator_memory_utilization_percentage")
	if utilization.Value != used.Value/total.Value*100 {
		t.Fatalf("utilization = %v, want used/total", utilization.Value)
	}
}
