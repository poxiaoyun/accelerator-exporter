package accelerator

import "testing"

// TestAscendOneSourceProducesBothTargetClasses covers the merge: a single
// npu-exporter endpoint yields physical NPU and vNPU targets from one scrape,
// instead of the same endpoint being configured and scraped twice.
func TestAscendOneSourceProducesBothTargetClasses(t *testing.T) {
	t.Parallel()
	physical := applyFixture(t, fixtures[2])
	physicalSample := findSample(t, physical.Samples, "accelerator_core_utilization_percentage")
	if physicalSample.Labels[LabelDeviceType] != DeviceTypeNPU ||
		physicalSample.Labels[LabelDeviceMode] != DeviceModePhysical ||
		physicalSample.Labels[LabelDeviceID] != "Ascend-0" {
		t.Fatalf("unexpected physical NPU identity: %#v", physicalSample.Labels)
	}

	virtual := applyFixture(t, fixtures[3])
	virtualSample := findSample(t, virtual.Samples, "accelerator_core_utilization_percentage")
	if virtualSample.Labels[LabelDeviceType] != DeviceTypeNPU ||
		virtualSample.Labels[LabelDeviceMode] != DeviceModeVirtual {
		t.Fatalf("unexpected vNPU identity: %#v", virtualSample.Labels)
	}
	// Spec: device_index has no meaning for a virtual target.
	if _, present := virtualSample.Labels[LabelDeviceIndex]; present {
		t.Errorf("a virtual target must not carry a position: %#v", virtualSample.Labels)
	}
}

// TestAscendUsesTheStableIdentifierNotTheChipIndex pins the assumed answer to
// the open question about Ascend identity: the samples carry both a whole-chip
// index and vdie_id, and only the latter may become a device_id.
func TestAscendUsesTheStableIdentifierNotTheChipIndex(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[2])
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	if sample.Labels[LabelDeviceID] != "Ascend-0" {
		t.Fatalf("device_id = %q, want the stable identifier", sample.Labels[LabelDeviceID])
	}
}

func TestAscendHealthStateFromTheNativeConvention(t *testing.T) {
	t.Parallel()
	// The exporter's documented convention is 1 = healthy, so a zero reading is
	// the failing one.
	healthy := ruleFor(t, RuleAscendNPU).Apply(Environment{Source: Source{Name: "npu", Rule: RuleAscendNPU, Labels: map[string]string{"node": "node-npu"}}},
		Families{"npu_chip_info_health_status": {{Labels: npuLabels(), Value: 1}}})
	if state := findSample(t, healthy.Samples, "accelerator_health_state"); state.Labels["state"] != HealthStateHealthy {
		t.Fatalf("state = %#v, want healthy", state)
	}
	if samples := findSamples(t, healthy.Samples, "accelerator_health_diagnostic_info"); len(samples) != 0 {
		t.Fatalf("a healthy device produces no diagnostic, got %#v", samples)
	}

	failing := ruleFor(t, RuleAscendNPU).Apply(Environment{Source: Source{Name: "npu", Rule: RuleAscendNPU, Labels: map[string]string{"node": "node-npu"}}},
		Families{"npu_chip_info_health_status": {{Labels: npuLabels(), Value: 0}}})
	state := findSample(t, failing.Samples, "accelerator_health_state")
	if state.Labels["state"] != HealthStateFault {
		t.Fatalf("state = %#v, want fault", state)
	}
	diagnostic := findSample(t, failing.Samples, "accelerator_health_diagnostic_info")
	if diagnostic.Labels["severity"] != SeverityCritical || diagnostic.Value != 1 {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
}

// TestAscendVNPUReportsTheMissingParent covers the one identity gap the source
// cannot fill: vnpu_pod_* names the vNPU but not the physical NPU it came from.
func TestAscendVNPUReportsTheMissingParent(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[3])
	sample := findSample(t, result.Samples, "accelerator_core_utilization_percentage")
	if _, present := sample.Labels[LabelParentDeviceID]; present {
		t.Fatalf("this source cannot supply a parent, so the label must be absent: %#v", sample.Labels)
	}
	drop, reasons := sample.Validate()
	if drop {
		t.Fatalf("the measurement must still be reported: %v", sample)
	}
	if len(reasons) != 2 || reasons[0] != ViolationDecimalDeviceID || reasons[1] != ViolationMissingParentID {
		t.Fatalf("reasons = %v, want the decimal identifier and the missing parent", reasons)
	}
}

func TestAscendOverallUtilizationKeepsItsVendorScope(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[2])
	sample := findSample(t, result.Samples, "accelerator_overall_utilization_percentage")
	if sample.Value != 41 {
		t.Fatalf("vendor utilization = %v, want the native value", sample.Value)
	}
	// 可比性由规范的「可比范围」属性承载，不压进指标名：这个指标是同一厂商内的，
	// 但名字只表达观测量。
	if _, ok := CanonicalMetric("accelerator_vendor_utilization_percentage"); ok {
		t.Fatal("the metric name must not encode comparability")
	}
}
