package accelerator

import (
	"math"
	"testing"
)

func TestDeriveMemoryMetricsFromUsedAndFree(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePhysical,
	}
	samples := deriveMemoryMetrics([]Sample{
		{Name: "accelerator_memory_used_bytes", Labels: base, Value: 4},
		{Name: "accelerator_memory_free_bytes", Labels: base, Value: 12},
	})
	total := findSample(t, samples, "accelerator_memory_total_bytes")
	if total.Value != 16 {
		t.Fatalf("total = %v, want used + free", total.Value)
	}
	utilization := findSample(t, samples, "accelerator_memory_utilization_percentage")
	if math.Abs(utilization.Value-25) > 0.0001 {
		t.Fatalf("utilization = %v, want 25", utilization.Value)
	}
}

func TestDeriveMemoryMetricsRequiresMeasuredFree(t *testing.T) {
	t.Parallel()
	for _, fixture := range fixtures[2:5] { // Ascend, vNPU and vGPU report used and total, but no free.
		t.Run(fixture.name, func(t *testing.T) {
			result := applyFixture(t, fixture)
			for _, sample := range result.Samples {
				if sample.Name == "accelerator_memory_free_bytes" {
					t.Fatalf("free must not be inferred from total and used: %#v", sample)
				}
			}
			used := findSample(t, result.Samples, "accelerator_memory_used_bytes")
			total := findSample(t, result.Samples, "accelerator_memory_total_bytes")
			utilization := findSample(t, result.Samples, "accelerator_memory_utilization_percentage")
			if utilization.Value != used.Value/total.Value*100 {
				t.Fatalf("utilization = %v, want used / total * 100", utilization.Value)
			}
		})
	}
}

// TestDeriveMemoryMetricsGroupsPerTargetNotPerLabelSet covers the defect where
// a vendor row that carried extra labels produced its own copy of every derived
// memory series.
func TestDeriveMemoryMetricsGroupsPerTargetNotPerLabelSet(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePhysical,
	}
	// Two rows for the same target, differing only by an enum label the metric
	// happens to carry.
	rowA := withLabel(base, "error_type", "xid")
	rowB := withLabel(base, "error_type", "ecc")
	samples := deriveMemoryMetrics([]Sample{
		{Name: "accelerator_memory_used_bytes", Labels: rowA, Value: 4},
		{Name: "accelerator_memory_used_bytes", Labels: rowB, Value: 4},
		{Name: "accelerator_memory_total_bytes", Labels: rowA, Value: 16},
	})
	derived := findSamples(t, samples, "accelerator_memory_utilization_percentage")
	if len(derived) != 1 {
		t.Fatalf("derived %d copies for one target: %s", len(derived), renderSamples(samples))
	}
	for name := range derived[0].Labels {
		if name == "error_type" {
			t.Errorf("a derived series must not inherit an unrelated enum label: %#v", derived[0].Labels)
		}
	}
}

func TestDeriveHealthStateTakesTheWorstSeverity(t *testing.T) {
	t.Parallel()
	labels := map[string]string{LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePhysical}
	derive := deriveHealthState("some_health_source")
	identity := identityFromLabels(labels)
	samples := derive(
		Families{"some_health_source": {{Value: 1}}},
		[]Sample{
			{Name: "accelerator_health_diagnostic_info", Labels: withLabel(labels, "severity", SeverityWarning), Value: 1},
			{Name: "accelerator_health_diagnostic_info", Labels: withLabel(labels, "severity", SeverityCritical), Value: 1},
		},
		map[string]Identity{identity.Key(): identity},
	)
	if len(samples) != 1 {
		t.Fatalf("samples = %s, want exactly one state", renderSamples(samples))
	}
	if samples[0].Labels["state"] != HealthStateFault || samples[0].Value != 1 {
		t.Fatalf("state = %#v, want fault with the contract value", samples[0])
	}
}

// TestDeriveHealthStateNeedsAnObservedTarget: a healthy device produces no
// diagnostic, so the target set is what resolves it to healthy.
func TestDeriveHealthStateNeedsAnObservedTarget(t *testing.T) {
	t.Parallel()
	labels := map[string]string{LabelNode: "node-a", LabelDeviceID: "GPU-1", LabelDeviceType: DeviceTypeGPU, LabelDeviceMode: DeviceModePhysical}
	derive := deriveHealthState("some_health_source")
	identity := identityFromLabels(labels)
	samples := derive(
		Families{"some_health_source": {{Value: 1}}},
		nil,
		map[string]Identity{identity.Key(): identity},
	)
	if len(samples) != 1 || samples[0].Labels["state"] != HealthStateHealthy {
		t.Fatalf("samples = %s, want a single healthy state", renderSamples(samples))
	}
}
