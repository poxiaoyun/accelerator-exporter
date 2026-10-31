package accelerator

import "testing"

func TestHygonDCUUnitsAndMissingMeasurements(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[7])
	for name, value := range map[string]float64{
		"accelerator_core_utilization_percentage":   75,
		"accelerator_memory_used_bytes":             8589934592,
		"accelerator_memory_total_bytes":            34359738368,
		"accelerator_memory_utilization_percentage": 25,
		"accelerator_temperature_celsius":           52,
		"accelerator_power_usage_watts":             180,
		"accelerator_power_limit_watts":             300,
	} {
		sample := findSample(t, result.Samples, name)
		if sample.Value != value || sample.Labels[LabelModel] != "bw100" ||
			sample.Labels[LabelDeviceType] != DeviceTypeDCU || sample.Labels[LabelVendor] != VendorHygon {
			t.Errorf("%s = %#v, want %v with DCU/BW100 classification", name, sample, value)
		}
	}
	for _, name := range []string{"accelerator_memory_free_bytes", "accelerator_health_state", "accelerator_memory_bandwidth_utilization_percentage"} {
		if got := findSamples(t, result.Samples, name); len(got) != 0 {
			t.Errorf("%s has no real source, got %#v", name, got)
		}
	}
	info := findSample(t, result.Samples, InventoryMetricName)
	if info.Labels[LabelPCIBusID] != "0000:f6:00.0" || info.Value != 1 {
		t.Fatalf("inventory = %#v", info)
	}
}

func TestHygonVDCULimitAndIdentity(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[8])
	for name, value := range map[string]float64{
		"accelerator_core_utilization_percentage":   40,
		"accelerator_memory_used_bytes":             4294967296,
		"accelerator_memory_total_bytes":            17179869184,
		"accelerator_memory_utilization_percentage": 25,
	} {
		sample := findSample(t, result.Samples, name)
		if sample.Value != value || sample.Labels[LabelDeviceID] != "T8R1380013061601.7" ||
			sample.Labels[LabelParentDeviceID] != "T8R1380013061601" || sample.Labels[LabelDeviceMode] != DeviceModeVirtual {
			t.Errorf("%s = %#v", name, sample)
		}
	}
	for _, name := range []string{"accelerator_temperature_celsius", "accelerator_power_usage_watts", "accelerator_power_limit_watts", "accelerator_memory_free_bytes"} {
		if got := findSamples(t, result.Samples, name); len(got) != 0 {
			t.Errorf("virtual target has no independent %s, got %#v", name, got)
		}
	}
}

func TestHygonAllocationUsesPCIAndProfileDeviceID(t *testing.T) {
	t.Parallel()
	physical, virtual := applyFixture(t, fixtures[7]), applyFixture(t, fixtures[8])
	allocations := []Allocation{
		{Namespace: "real-ns", Pod: "physical", PodUID: "puid", Container: "worker", NativeResource: "hygon.com/dcu", NativeDeviceID: "0000:f6:00.0"},
		{Namespace: "real-ns", Pod: "virtual", PodUID: "vuid", Container: "worker", NativeResource: "hygon.com/dcu-share-8c-16g", NativeDeviceID: "vdev7"},
		// Neither a serial under the PCI resource nor the wrong virtual profile joins.
		{Namespace: "real-ns", Pod: "wrong-serial", PodUID: "uid", Container: "worker", NativeResource: "hygon.com/dcu", NativeDeviceID: "T8R1380013061601"},
		{Namespace: "real-ns", Pod: "wrong-profile", PodUID: "uid", Container: "worker", NativeResource: "hygon.com/dcu-share-8c-8g", NativeDeviceID: "vdev7"},
	}
	got := BuildPodInfo(allocations, physical.AllocationIndex, virtual.AllocationIndex)
	if len(got) != 2 || got[0].Labels[LabelDeviceID] != "T8R1380013061601" || got[1].Labels[LabelDeviceID] != "T8R1380013061601.7" {
		t.Fatalf("allocation relations = %#v", got)
	}
	if got := BuildPodInfo(nil, physical.AllocationIndex, virtual.AllocationIndex); len(got) != 0 {
		t.Fatalf("vendor workload labels must not create ownership: %#v", got)
	}
}

func TestHygonVDCUMissingOrInvalidLimitStaysMissing(t *testing.T) {
	t.Parallel()
	for _, limit := range []string{"", "NaN", "-1", "0", "1.5", "18446744073709551616"} {
		t.Run(limit, func(t *testing.T) {
			raw := withLabel(vdcuLabels(), "vdcu_memory_cap", limit)
			result := ruleFor(t, RuleHygonDCU).Apply(Environment{Source: fixtures[8].source},
				Families{"vdcu_usedmemory_bytes": {{Labels: raw, Value: 4294967296}}})
			if len(findSamples(t, result.Samples, "accelerator_memory_total_bytes")) != 0 ||
				len(findSamples(t, result.Samples, "accelerator_memory_utilization_percentage")) != 0 {
				t.Fatalf("invalid limit %q must remain missing: %#v", limit, result.Samples)
			}
			if findSample(t, result.Samples, "accelerator_memory_used_bytes").Value != 4294967296 {
				t.Fatal("measured used memory was lost")
			}
		})
	}
}

func TestHygonVDCUParentsDoNotCollapse(t *testing.T) {
	t.Parallel()
	rule := ruleFor(t, RuleHygonDCU)
	result := rule.Apply(Environment{Source: fixtures[8].source}, Families{
		"vdcu_usedmemory_bytes": {
			{Labels: vdcuLabels(), Value: 1},
			{Labels: withLabel(vdcuLabels(), "device_id", "another-parent"), Value: 2},
		},
	})
	if len(result.Identities) != 2 || len(findSamples(t, result.Samples, InventoryMetricName)) != 2 {
		t.Fatalf("same index on different parents collapsed: %#v", result)
	}
}

func TestHygonVDCUConflictingLimitsStayMissing(t *testing.T) {
	t.Parallel()
	result := ruleFor(t, RuleHygonDCU).Apply(Environment{Source: fixtures[8].source}, Families{
		"vdcu_usedmemory_bytes": {{Labels: vdcuLabels(), Value: 1}},
		"vdcu_utilizationrate":  {{Labels: withLabel(vdcuLabels(), "vdcu_memory_cap", "8589934592"), Value: 40}},
	})
	if len(findSamples(t, result.Samples, "accelerator_memory_total_bytes")) != 0 {
		t.Fatalf("conflicting limits must not select an arbitrary capacity: %#v", result.Samples)
	}
}
