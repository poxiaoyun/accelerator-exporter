package accelerator

import "testing"

func TestBuildPodInfoJoinsAllocationFactsToTargets(t *testing.T) {
	t.Parallel()
	gpu := applyFixture(t, fixtures[0])
	mig := applyFixture(t, fixtures[1])

	allocations := []Allocation{
		{Namespace: "train", Pod: "job-0", PodUID: "uid-0", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-1"},
		// Allocated under a resource no rule declares: an unresolvable
		// allocation must produce nothing rather than a sentinel target.
		{Namespace: "train", Pod: "job-0", PodUID: "uid-0", Container: "main", NativeResource: "example.com/fpga", NativeDeviceID: "FPGA-1"},
		// A device identifier no source reported.
		{Namespace: "train", Pod: "job-1", PodUID: "uid-1", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-9"},
		// A partition allocation carries a MIG device identifier, which DCGM
		// does not report, so the parent GPU must not be substituted for it.
		{Namespace: "train", Pod: "job-2", PodUID: "uid-2", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-1.3"},
		// No UID: the relation key would be ambiguous across Pod lifetimes.
		{Namespace: "train", Pod: "job-3", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-1"},
	}

	samples := BuildPodInfo(allocations, gpu.AllocationIndex, mig.AllocationIndex)
	if len(samples) != 1 {
		t.Fatalf("samples = %s, want only the resolvable allocation", renderSamples(samples))
	}
	sample := samples[0]
	if sample.Name != PodInfoMetricName || sample.Value != 1 {
		t.Fatalf("unexpected relation sample: %#v", sample)
	}
	want := map[string]string{
		LabelNode:      "node-gpu",
		LabelDeviceID:  "GPU-1",
		LabelNamespace: "train",
		LabelPod:       "job-0",
		LabelPodUID:    "uid-0",
		LabelContainer: "main",
	}
	if len(sample.Labels) != len(want) {
		t.Fatalf("relation labels = %#v, want exactly the declared key", sample.Labels)
	}
	for name, value := range want {
		if sample.Labels[name] != value {
			t.Errorf("%s = %q, want %q", name, sample.Labels[name], value)
		}
	}
}

// TestBuildPodInfoKeepsEveryContainerOfASharedTarget: several containers of one
// Pod can bind the same target, and the container label is what keeps those
// relationships apart.
func TestBuildPodInfoKeepsEveryContainerOfASharedTarget(t *testing.T) {
	t.Parallel()
	gpu := applyFixture(t, fixtures[0])
	samples := BuildPodInfo([]Allocation{
		{Namespace: "train", Pod: "job-0", PodUID: "uid-0", Container: "main", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-1"},
		{Namespace: "train", Pod: "job-0", PodUID: "uid-0", Container: "sidecar", NativeResource: "nvidia.com/gpu", NativeDeviceID: "GPU-1"},
	}, gpu.AllocationIndex)
	if len(samples) != 2 {
		t.Fatalf("samples = %s, want one per container", renderSamples(samples))
	}
}

func TestBuildPodInfoWithoutAllocationsProducesNothing(t *testing.T) {
	t.Parallel()
	gpu := applyFixture(t, fixtures[0])
	if samples := BuildPodInfo(nil, gpu.AllocationIndex); len(samples) != 0 {
		t.Fatalf("no allocation source means no relation layer, got %s", renderSamples(samples))
	}
}

// TestAllocationIndexRespectsDeviceMode: a whole-device allocation must not
// resolve onto a partition that reports the same native identifier.
func TestAllocationIndexRespectsDeviceMode(t *testing.T) {
	t.Parallel()
	result := applyFixture(t, fixtures[1])
	if len(result.AllocationIndex) != 0 {
		// The MIG fixture reports the parent UUID on its samples, but its
		// identity is a partition, and the only declared allocation is a
		// whole-device one.
		t.Fatalf("a partition must not be indexed as a whole device: %#v", result.AllocationIndex)
	}
}
