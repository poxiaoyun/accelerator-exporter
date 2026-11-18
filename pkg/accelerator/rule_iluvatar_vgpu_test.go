package accelerator

import (
	"strings"
	"testing"
)

func TestIluvatarVirtualMetricsResolveParentsAndAllocation(t *testing.T) {
	env := iluvatarEnvironment()
	env.Source.Rule = RuleIluvatarVGPU
	env.Inventory = BuildInventory([]InventoryInput{{Source: iluvatarEnvironment().Source, Families: Families{
		"ix_gpu_utilization": {{Labels: map[string]string{"gpu": "0", "uuid": "GPU-a", "name": "MRC-V100-0x1B"}}, {Labels: map[string]string{"gpu": "1", "uuid": "GPU-b", "name": "MRC-V100-0x1B"}}},
	}}})
	env.Allocations = []Allocation{
		{Namespace: "ns", Pod: "real", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.ai/MRC-V100.vCore", NativeDeviceID: "iluvatar.ai/MRC-V100.vCore-0"},
		{Namespace: "ns", Pod: "real", PodUID: "uid", Container: "worker", NativeResource: "iluvatar.ai/MRC-V100.vMem", NativeDeviceID: "iluvatar.ai/MRC-V100.vMem-268435456-0"},
	}
	labels := func(index string) map[string]string {
		return map[string]string{"namespace": "ns", "pod_name": "real", "container_name": "worker", "modelName": "MRC-V100-0x1B", "gpu": index}
	}
	raw := Families{
		"container_gpu_utilization":  {{Labels: labels("gpu0"), Value: 25}, {Labels: labels("gpu1"), Value: 50}, {Labels: labels("total"), Value: 75}},
		"container_gpu_memory_total": {{Labels: labels("gpu0"), Value: 1024}, {Labels: labels("total"), Value: 1024}},
	}
	result := ruleFor(t, RuleIluvatarVGPU).Apply(env, raw)
	if len(result.Identities) != 2 {
		t.Fatalf("identities=%#v", result.Identities)
	}
	used := findSample(t, result.Samples, "accelerator_memory_used_bytes")
	if used.Value != 1024*bytesPerMiB || used.Labels[LabelParentDeviceID] != "GPU-a" || used.Labels[LabelDeviceMode] != DeviceModeVirtual || used.Labels[LabelModel] != "mrc-v100" {
		t.Fatalf("used=%#v", used)
	}
	if len(findSamples(t, result.Samples, "accelerator_memory_total_bytes")) != 0 {
		t.Fatal("used memory became slice capacity")
	}
	bindings := BuildPodInfo(env.Allocations, result.AllocationIndex)
	if len(bindings) != 2 {
		t.Fatalf("multi-parent bindings=%#v", bindings)
	}
	for _, binding := range bindings {
		if binding.Labels[LabelPodUID] != "uid" || !strings.HasSuffix(binding.Labels[LabelDeviceID], "GPU-a") && !strings.HasSuffix(binding.Labels[LabelDeviceID], "GPU-b") {
			t.Fatalf("binding=%#v", binding)
		}
	}
	// Exporter labels cannot establish ownership without kubelet facts.
	env.Allocations = nil
	result = ruleFor(t, RuleIluvatarVGPU).Apply(env, raw)
	if len(result.AllocationIndex) != 0 {
		t.Fatal("vendor workload labels became allocation facts")
	}
}

func TestIluvatarVirtualDoesNotGuessMissingOrAmbiguousParent(t *testing.T) {
	input := InventoryInput{Source: iluvatarEnvironment().Source, Families: Families{
		"ix_gpu_utilization": {{Labels: map[string]string{"gpu": "0", "uuid": "GPU-a"}}, {Labels: map[string]string{"gpu": "0", "uuid": "GPU-b"}}},
	}}
	for _, inventory := range []Inventory{nil, BuildInventory([]InventoryInput{input})} {
		env := iluvatarEnvironment()
		env.Inventory = inventory
		env.Allocations = []Allocation{{Namespace: "ns", Pod: "p", PodUID: "uid", Container: "c", NativeResource: "iluvatar.ai/MRC-V100.vCore", NativeDeviceID: "core-0"}}
		result := ruleFor(t, RuleIluvatarVGPU).Apply(env, Families{"container_gpu_utilization": {{Labels: map[string]string{"namespace": "ns", "pod_name": "p", "container_name": "c", "modelName": "MRC-V100", "gpu": "gpu0"}, Value: 50}}})
		for _, identity := range result.Identities {
			if identity.DeviceID != "" || identity.ParentDeviceID != "" {
				t.Fatalf("guessed parent=%#v", identity)
			}
		}
		if len(BuildPodInfo(env.Allocations, result.AllocationIndex)) != 0 {
			t.Fatal("bound unresolved parent")
		}
	}
}
