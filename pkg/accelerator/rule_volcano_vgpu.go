package accelerator

import "strings"

// This file interprets the Volcano vGPU monitor. Its one endpoint exposes two
// observation target classes: the slices it carves by software
// (device_mode=virtual) and the physical cards those slices come from
// (device_mode=physical). One Rule carries both, so the endpoint is scraped once
// instead of twice with half the result discarded.
//
// A slice has no independent temperature, power, throttle reason or hardware
// error, and the contract restricts those to physical devices; a consumer reads
// the card's own readings through parent_device_id instead. The card class
// carries core utilization even on a node that runs no DCGM.

// volcanoVGPUResource is the extended resource a slice is allocated under. It is
// also the only place the slice identifier exists, which makes this rule the one
// in the repository whose identity cannot be resolved from sample labels alone.
const volcanoVGPUResource = "volcano.sh/vgpu-number"

var volcanoVGPURule = Rule{
	Name:   RuleVolcanoVGPU,
	Vendor: VendorNVIDIA,
	Targets: []Target{
		{
			Identify: identifyVolcanoVGPU,
			Metrics: []Metric{
				{Source: "Device_utilization_desc_of_container", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
				// Both native values are already bytes.
				{Source: "vGPU_device_memory_usage_in_bytes", Canonical: "accelerator_memory_used_bytes", Factor: 1},
				// The limit the slice was allocated, not the capacity of the
				// physical GPU. Limits of several slices must never be summed.
				{Source: "vGPU_device_memory_limit_in_bytes", Canonical: "accelerator_memory_total_bytes", Factor: 1},
			},
			// The slice identifier the relation layer must join on is the same value
			// the identity takes, so both call the same resolution.
			NativeResources: []NativeResource{{
				Names:    []string{volcanoVGPUResource},
				Mode:     DeviceModeVirtual,
				DeviceID: vgpuSliceDeviceID,
			}},
		},
		{
			// The card itself. This is the reading a consumer looks up through
			// parent_device_id, and on a node without DCGM this is the only place
			// it exists. When DCGM is configured too, both rules map core
			// utilization onto the same target and the source configured first
			// wins; see the ordering note in README.
			Identify: identifyVolcanoVGPUCard,
			Metrics: []Metric{
				// HostGPUMemoryUsage includes driver-reserved memory and cannot
				// satisfy the contract's used-memory semantics; see DESIGN.md §9.2.
				{Source: "HostCoreUtilization", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
			},
			// A whole-card allocation under nvidia.com/gpu names this card, so it
			// resolves here too and the relation layer still works without DCGM.
			NativeResources: []NativeResource{{
				Names:    []string{nvidiaGPUResource},
				Mode:     DeviceModePhysical,
				DeviceID: func(_ Environment, raw map[string]string) string { return first(raw, "deviceuuid", "UUID", "uuid") },
			}},
		},
	},
}

// identifyVolcanoVGPU resolves the Volcano vGPU slice observation target.
//
// The physical GPU a slice belongs to is on every sample as deviceuuid. The
// slice itself is not: vdeviceid is the ordinal of the slice inside its
// container, so a container holding a single slice reports zero whichever slice
// it was given, and using it as the identifier collapses several slices of one
// GPU onto one observation target that is then silently deduplicated.
func identifyVolcanoVGPU(env Environment, raw map[string]string) Identity {
	parent := first(raw, "deviceuuid", "UUID", "uuid")
	identity := Identity{
		Node:           firstNonEmpty(first(raw, "node", "Hostname", "hostname"), env.Source.Labels[LabelNode]),
		Model:          firstNonEmpty(first(raw, "model", "modelName", "model_name"), env.Source.Labels[LabelModel]),
		DeviceType:     DeviceTypeGPU,
		DeviceMode:     DeviceModeVirtual,
		Vendor:         VendorNVIDIA,
		DeviceID:       vgpuSliceDeviceID(env, raw),
		ParentDeviceID: parent,
	}
	// The vGPU families omit node and model; take them from the physical device
	// that DCGM reported for the same identifier.
	if info, ok := env.Inventory[parent]; ok {
		identity.Node = firstNonEmpty(identity.Node, info.Node)
		identity.Model = firstNonEmpty(identity.Model, info.Model)
	}
	return identity
}

// identifyVolcanoVGPUCard resolves the physical card a vGPU belongs to.
//
// The monitor labels its card readings with deviceuuid and a device index, and
// device_index is not emitted: the specification makes it meaningful only for a
// partition, and a physical target carries no position.
//
// Node and model come from the inventory or from the source's pinned labels, the
// same way a slice gets them, because these families carry neither. Only model
// may be pinned; node is always the node this process runs on.
func identifyVolcanoVGPUCard(env Environment, raw map[string]string) Identity {
	device := first(raw, "deviceuuid", "UUID", "uuid")
	identity := Identity{
		Node:       firstNonEmpty(first(raw, "node", "Hostname", "hostname"), env.Source.Labels[LabelNode]),
		Model:      firstNonEmpty(first(raw, "model", "modelName", "model_name"), env.Source.Labels[LabelModel]),
		DeviceType: DeviceTypeGPU,
		DeviceMode: DeviceModePhysical,
		Vendor:     VendorNVIDIA,
		DeviceID:   device,
	}
	if info, ok := env.Inventory[device]; ok {
		identity.Node = firstNonEmpty(identity.Node, info.Node)
		identity.Model = firstNonEmpty(identity.Model, info.Model)
	}
	return identity
}

// vgpuSliceDeviceID resolves the slice identifier of one monitor sample out of
// the device allocation facts of the round.
//
// The identifier is the one the device plugin allocated, <parent UUID>-<slot>.
// It is used verbatim rather than recomposed from a sequence number: it is the
// vendor's own instance identifier, and keeping it unchanged is what lets the
// relation layer join an allocation onto this target with no translation.
//
// The pod labels of the sample only select which allocation record to read. The
// identifier that reaches the series comes out of that record, never out of the
// sample, so the vendor's workload labels are not the authority on ownership.
//
// It is returned only when exactly one allocation matches, because a wrong
// identifier is worse than a missing one. No match means the allocation source
// did not report this container, either because it is unavailable or because
// the Pod is already gone. Several matches mean the container holds more than
// one slice of the same GPU while vdeviceid, an ordinal inside the container,
// cannot say which of them a sample belongs to. Both cases leave device_id
// empty so contract validation reports the identity gap, instead of the
// exporter guessing a slice and attributing the reading to the wrong one.
func vgpuSliceDeviceID(env Environment, raw map[string]string) string {
	parent := first(raw, "deviceuuid", "UUID", "uuid")
	namespace := first(raw, "podnamespace", "namespace")
	pod := first(raw, "podname", "pod")
	container := first(raw, "ctrname", "container")
	if parent == "" || namespace == "" || pod == "" || container == "" {
		return ""
	}
	match := ""
	for _, allocation := range env.Allocations {
		if allocation.NativeResource != volcanoVGPUResource {
			continue
		}
		if allocation.Namespace != namespace || allocation.Pod != pod || allocation.Container != container {
			continue
		}
		if !strings.HasPrefix(allocation.NativeDeviceID, parent+"-") {
			continue
		}
		if match != "" {
			return ""
		}
		match = allocation.NativeDeviceID
	}
	return match
}
