package accelerator

import "testing"

// TestCanonicalMetricInventory pins the contract's metric inventory.
//
// The inventory is what downstream consumers, dashboards and alert rules are
// written against, so it is asserted as one list: a rename, a deletion or an
// accidental addition all show up here instead of silently changing what the
// platform exposes.
func TestCanonicalMetricInventory(t *testing.T) {
	t.Parallel()
	want := []string{
		"accelerator_core_utilization_percentage",
		"accelerator_errors_total",
		"accelerator_health_diagnostic_info",
		"accelerator_health_state",
		"accelerator_info",
		"accelerator_io_utilization_percentage",
		"accelerator_memory_bandwidth_utilization_percentage",
		"accelerator_memory_free_bytes",
		"accelerator_memory_oom_events_total",
		"accelerator_memory_total_bytes",
		"accelerator_memory_used_bytes",
		"accelerator_memory_utilization_percentage",
		"accelerator_overall_utilization_percentage",
		"accelerator_pod_info",
		"accelerator_power_limit_watts",
		"accelerator_power_usage_watts",
		"accelerator_temperature_celsius",
		"accelerator_throttle_reason_info",
	}
	got := CanonicalMetricNames()
	if len(got) != len(want) {
		t.Fatalf("contract has %d metrics, want %d:\n%v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("contract metrics = %v, want %v", got, want)
		}
	}
}

func TestCanonicalMetricTypesAndLayers(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"accelerator_errors_total", "accelerator_memory_oom_events_total"} {
		metadata, ok := CanonicalMetric(name)
		if !ok || metadata.Type != MetricTypeCounter {
			t.Errorf("%s = %#v, %v; want a counter", name, metadata, ok)
		}
	}
	for name, metadata := range canonicalMetrics {
		if metadata.Help == "" || metadata.Unit == "" || metadata.ValueDomain == "" || metadata.Layer == "" {
			t.Errorf("%s is missing contract metadata: %#v", name, metadata)
		}
		if name == InventoryMetricName && !metadata.Inventory {
			t.Errorf("%s must be marked as the inventory source", name)
		}
	}
	// Spec: temperature, power, power limit, throttle reasons and hardware
	// errors describe the physical device.
	for _, name := range []string{
		"accelerator_temperature_celsius",
		"accelerator_power_usage_watts",
		"accelerator_power_limit_watts",
		"accelerator_throttle_reason_info",
		"accelerator_errors_total",
	} {
		metadata, _ := CanonicalMetric(name)
		if len(metadata.Modes) != 1 || metadata.Modes[0] != DeviceModePhysical {
			t.Errorf("%s must be physical-device only, got modes %v", name, metadata.Modes)
		}
	}
}

func TestValidateReportsGapsWithoutDroppingMeasurements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sample  Sample
		drop    bool
		reasons []string
	}{
		{
			name: "conformant physical device",
			sample: Sample{Name: "accelerator_temperature_celsius", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePhysical,
			}},
		},
		{
			name:    "unknown metric",
			sample:  Sample{Name: "accelerator_batch_size_count"},
			drop:    true,
			reasons: []string{ViolationUnknownMetric},
		},
		{
			name: "physical-only metric on a partition",
			sample: Sample{Name: "accelerator_temperature_celsius", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "GPU-1.3", LabelParentDeviceID: "GPU-1",
				LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePartition,
			}},
			drop:    true,
			reasons: []string{ViolationInapplicableMode},
		},
		{
			name: "identity gap is reported, not dropped",
			sample: Sample{Name: "accelerator_core_utilization_percentage", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePhysical,
			}},
			reasons: []string{ViolationMissingIdentity},
		},
		{
			name: "an instance without a parent is reported, not dropped",
			sample: Sample{Name: "accelerator_core_utilization_percentage", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "vnpu-1", LabelDeviceType: DeviceTypeNPU, LabelDeviceMode: DeviceModeVirtual,
			}},
			reasons: []string{ViolationMissingParentID},
		},
		{
			name: "a position used as an identity is reported",
			sample: Sample{Name: "accelerator_core_utilization_percentage", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "0", LabelDeviceType: DeviceTypeNPU, LabelDeviceMode: DeviceModePhysical,
			}},
			reasons: []string{ViolationDecimalDeviceID},
		},
		{
			name: "a position label on a whole device is reported",
			sample: Sample{Name: "accelerator_core_utilization_percentage", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU,
				LabelDeviceMode: DeviceModePhysical, LabelDeviceIndex: "3",
			}},
			reasons: []string{ViolationUnexpectedIndex},
		},
		{
			name: "a health state must carry a declared state",
			sample: Sample{Name: "accelerator_health_state", Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU,
				LabelDeviceMode: DeviceModePhysical, "state": "broken",
			}},
			drop:    true,
			reasons: []string{ViolationInvalidEnum},
		},
		{
			name: "the relation layer needs its full key",
			sample: Sample{Name: PodInfoMetricName, Labels: map[string]string{
				LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelNamespace: "train", LabelPod: "job-0",
			}},
			drop:    true,
			reasons: []string{ViolationMissingRelationPart},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			drop, reasons := test.sample.Validate()
			if drop != test.drop {
				t.Errorf("drop = %v, want %v", drop, test.drop)
			}
			if len(reasons) != len(test.reasons) {
				t.Fatalf("reasons = %v, want %v", reasons, test.reasons)
			}
			for index := range test.reasons {
				if reasons[index] != test.reasons[index] {
					t.Fatalf("reasons = %v, want %v", reasons, test.reasons)
				}
			}
		})
	}
}
