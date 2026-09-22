package accelerator

import "testing"

// Every label, value and slot number in this file comes from the capture the
// fixtures describe (node "ubuntu", one RTX 3080, three slices live on it). The
// rule's own tests must not invent a label value: an invented vdeviceid is what
// hid the defect these tests now cover.

func TestVolcanoVGPUSliceIdentityComesFromTheAllocationFact(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	want := map[string]string{
		// The slot is on the allocation fact and nowhere else; vdeviceid is 0.
		LabelDeviceID:       vgpuParentUUID + "-1",
		LabelDeviceMode:     DeviceModeVirtual,
		LabelDeviceType:     DeviceTypeGPU,
		LabelParentDeviceID: vgpuParentUUID,
		LabelNode:           "ubuntu",
		LabelModel:          "NVIDIA GeForce RTX 3080",
	}
	for _, name := range []string{LabelDeviceID, LabelDeviceMode, LabelDeviceType, LabelParentDeviceID, LabelModel, LabelNode} {
		if sample.Labels[name] != want[name] {
			t.Errorf("%s = %q, want %q", name, sample.Labels[name], want[name])
		}
	}
}

// TestVolcanoVGPUSlicesOfOneCardDoNotCollapse is the regression for the defect
// this rule had. device_id used to be vdeviceid, the ordinal of the slice inside
// its container, so three containers each holding one slice of the same card all
// computed the same (node, device_id) and were deduplicated down to one series:
// two of the three slices disappeared without an error anywhere.
func TestVolcanoVGPUSlicesOfOneCardDoNotCollapse(t *testing.T) {
	t.Parallel()
	containers := [][3]string{
		{"demo", "test-zk-vllm-5cf7947486-pzk2l", "vllm"},
		{"default", "ae-vgpu-1", "holder"},
		{"default", "ae-vgpu-2", "holder"},
	}
	families := Families{}
	for _, container := range containers {
		families["vGPU_device_memory_usage_in_bytes"] = append(
			families["vGPU_device_memory_usage_in_bytes"],
			RawSample{Labels: vgpuSliceLabelsFor(container[0], container[1], container[2]), Value: 1},
		)
	}
	result := ruleFor(t, RuleVolcanoVGPU).Apply(Environment{
		Source: Source{Name: "vgpu", Rule: RuleVolcanoVGPU}, Allocations: vgpuAllocations(),
	}, families)

	samples := findSamples(t, result.Samples, "accelerator_memory_used_bytes")
	if len(samples) != len(containers) {
		t.Fatalf("each slice of the card must be its own target, got %d series: %#v", len(samples), samples)
	}
	got := map[string]bool{}
	for _, sample := range samples {
		got[sample.Labels[LabelDeviceID]] = true
		if sample.Labels[LabelParentDeviceID] != vgpuParentUUID {
			t.Errorf("parent = %q, want the card UUID", sample.Labels[LabelParentDeviceID])
		}
	}
	for _, want := range []string{vgpuParentUUID + "-1", vgpuParentUUID + "-2", vgpuParentUUID + "-6"} {
		if !got[want] {
			t.Errorf("no series for slice %s, got %v", want, got)
		}
	}
}

// TestVolcanoVGPUWithoutTheAllocationSourceProducesNoSeries: the slice identity
// exists only in the allocation fact, so a round that could not read it must not
// produce a series with a guessed identifier. The samples are emitted without a
// device_id and validation reports the identity gap, which is the pipeline's
// rule for every incomplete identity: the reading is worth keeping, but no
// consumer filtering on device_id will match it.
func TestVolcanoVGPUWithoutTheAllocationSourceProducesNoSeries(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixture{
		name:     "volcano-vgpu-without-allocations",
		source:   Source{Name: "vgpu", Rule: RuleVolcanoVGPU},
		families: Families{"vGPU_device_memory_usage_in_bytes": {{Labels: vgpuSliceLabels(), Value: 1}}},
	})
	if len(result.Samples) == 0 {
		t.Fatal("the measurement must still be reported, so the gap is visible")
	}
	for _, sample := range result.Samples {
		if sample.Labels[LabelDeviceID] != "" {
			t.Errorf("no allocation source can still identify a slice: %#v", sample)
		}
		if _, reasons := sample.Validate(); !contains(reasons, ViolationMissingIdentity) {
			t.Errorf("the identity gap must be reported: %#v", sample)
		}
	}
}

// TestVolcanoVGPUAmbiguousSliceProducesNoSeries: when one container holds more
// than one slice of the same card, vdeviceid cannot say which of them a sample
// belongs to. Pairing them by order would be a guess, so no identifier is
// produced and the gap is reported instead.
func TestVolcanoVGPUAmbiguousSliceProducesNoSeries(t *testing.T) {
	t.Parallel()
	allocations := append(vgpuAllocations(),
		Allocation{
			Namespace: "demo", Pod: "test-zk-vllm-5cf7947486-pzk2l", PodUID: "uid-vllm", Container: "vllm",
			NativeResource: volcanoVGPUResource, NativeDeviceID: vgpuParentUUID + "-3",
		},
	)
	result := ruleFor(t, RuleVolcanoVGPU).Apply(Environment{
		Source: Source{Name: "vgpu", Rule: RuleVolcanoVGPU}, Allocations: allocations,
	}, Families{"vGPU_device_memory_usage_in_bytes": {{Labels: vgpuSliceLabels(), Value: 1}}})

	for _, sample := range result.Samples {
		if sample.Labels[LabelDeviceID] != "" {
			t.Fatalf("an ambiguous slice must not be identified by guesswork: %#v", sample)
		}
	}
}

// TestVolcanoVGPUAllocationResolvesToTheSlicedTarget covers the relation layer:
// the allocation fact names the slice with the same identifier the device layer
// uses, so an allocation joins onto the target the monitor reported.
func TestVolcanoVGPUAllocationResolvesToTheSlicedTarget(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	samples := BuildPodInfo(vgpuAllocations(), result.AllocationIndex)
	sample := findSampleWithLabel(t, samples, PodInfoMetricName, LabelPod, "test-zk-vllm-5cf7947486-pzk2l")
	want := map[string]string{
		LabelNode:      "ubuntu",
		LabelDeviceID:  vgpuParentUUID + "-1",
		LabelNamespace: "demo",
		LabelContainer: "vllm",
	}
	for name, value := range want {
		if sample.Labels[name] != value {
			t.Errorf("%s = %q, want %q", name, sample.Labels[name], value)
		}
	}
}

// TestVolcanoVGPUReportsTheCardsOwnReadings: the same endpoint also reports the
// card, and on a node that runs no DCGM this is where the readings a consumer
// looks up through parent_device_id come from. The card target carries no
// device_index, because a position label is only meaningful for a partition.
func TestVolcanoVGPUReportsTheCardsOwnReadings(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])

	card := findSampleWithLabel(t, result.Samples, "accelerator_memory_used_bytes", LabelDeviceMode, DeviceModePhysical)
	if card.Value != 5044830208 {
		t.Fatalf("card memory used = %v, want the reading the monitor reported", card.Value)
	}
	want := map[string]string{
		LabelDeviceID:   vgpuParentUUID,
		LabelDeviceMode: DeviceModePhysical,
		LabelDeviceType: DeviceTypeGPU,
		LabelNode:       "ubuntu",
		LabelModel:      "NVIDIA GeForce RTX 3080",
	}
	for name, value := range want {
		if card.Labels[name] != value {
			t.Errorf("%s = %q, want %q", name, card.Labels[name], value)
		}
	}
	for _, name := range []string{LabelDeviceIndex, LabelParentDeviceID} {
		if card.Labels[name] != "" {
			t.Errorf("a physical target must not carry %s: %#v", name, card.Labels)
		}
	}

	// The slice and the card are different observation targets even though both
	// report a used-memory series, so the card's reading must not be the slice's.
	slice := findSampleWithLabel(t, result.Samples, "accelerator_memory_used_bytes", LabelDeviceMode, DeviceModeVirtual)
	if slice.Value == card.Value {
		t.Fatalf("the slice and the card reported the same usage: %#v", slice)
	}
}

// TestVolcanoVGPUCardAllocationResolvesWithoutDCGM: a whole card allocated under
// nvidia.com/gpu resolves onto the card target this rule produces, so the
// relation layer of a node without DCGM is not left empty.
func TestVolcanoVGPUCardAllocationResolvesWithoutDCGM(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	allocations := []Allocation{{
		Namespace: "train", Pod: "exclusive-job", PodUID: "uid-exclusive", Container: "main",
		NativeResource: nvidiaGPUResource, NativeDeviceID: vgpuParentUUID,
	}}
	samples := BuildPodInfo(allocations, result.AllocationIndex)
	sample := findSampleWithLabel(t, samples, PodInfoMetricName, LabelPod, "exclusive-job")
	if sample.Labels[LabelDeviceID] != vgpuParentUUID || sample.Labels[LabelNode] != "ubuntu" {
		t.Fatalf("a whole-card allocation did not resolve to the card target: %#v", sample)
	}
}

// TestVolcanoVGPUInheritsHostMetadata covers why the inventory exists: the vGPU
// families omit node and model, so they come from the physical device that the
// DCGM source reported for the same card.
func TestVolcanoVGPUInheritsHostMetadata(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleVolcanoVGPU).Apply(Environment{
		Source:      Source{Name: "vgpu", Rule: RuleVolcanoVGPU},
		Inventory:   Inventory{vgpuParentUUID: {Node: "ubuntu", Model: "NVIDIA GeForce RTX 3080"}},
		Allocations: vgpuAllocations(),
	}, Families{"vGPU_device_memory_usage_in_bytes": {{Labels: vgpuSliceLabels(), Value: 1}}})

	sample := findSample(t, result.Samples, "accelerator_memory_used_bytes")
	if sample.Labels[LabelNode] != "ubuntu" || sample.Labels[LabelModel] != "NVIDIA GeForce RTX 3080" {
		t.Fatalf("the host metadata was not inherited: %#v", sample.Labels)
	}
}

// TestVolcanoVGPUReportsNoPhysicalReadingOnASlice covers the contract
// restriction: a slice has no independent temperature or power of its own. The
// readings are facts of the card, so a consumer follows parent_device_id to the
// physical target instead of the exporter repeating them on the slice.
func TestVolcanoVGPUReportsNoPhysicalReadingOnASlice(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	for _, name := range []string{
		"accelerator_temperature_celsius",
		"accelerator_power_usage_watts",
		"accelerator_errors_total",
		"accelerator_throttle_reason_info",
		"accelerator_health_state",
	} {
		for _, sample := range findSamples(t, result.Samples, name) {
			if sample.Labels[LabelDeviceMode] == DeviceModeVirtual {
				t.Errorf("a vGPU slice must not report %s: %#v", name, sample)
			}
		}
	}
}

// TestVolcanoVGPUMemoryIsNotTakenFromTheParent: the shortcut of reading the
// parent device does not extend to memory. The card's used memory covers all of
// its slices at once, while a slice has its own limit, so the slice must report
// its own values and the limit must never be summed across slices.
func TestVolcanoVGPUMemoryIsNotTakenFromTheParent(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[4])
	total := findSample(t, result.Samples, "accelerator_memory_total_bytes")
	if total.Value != 4294967296 {
		t.Fatalf("memory total = %v, want the slice limit, not the card capacity", total.Value)
	}
	used := findSample(t, result.Samples, "accelerator_memory_used_bytes")
	if used.Value != 4230087680 {
		t.Fatalf("memory used = %v, want the slice's own usage", used.Value)
	}
	utilization := findSample(t, result.Samples, "accelerator_memory_utilization_percentage")
	if utilization.Value != used.Value/total.Value*100 {
		t.Fatalf("utilization = %v, want used/total", utilization.Value)
	}
}
