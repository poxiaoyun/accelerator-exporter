package accelerator

// This file interprets the Volcano vGPU monitor. It reports per-slice usage for
// software-partitioned GPU instances, so its observation targets are always
// device_mode=virtual and it never supplies a physical-device reading: the
// contract restricts temperature, power, throttle reasons and hardware errors
// to physical devices, and a slice has no independent reading of its own.

var volcanoVGPURule = Rule{
	Name:   RuleVolcanoVGPU,
	Vendor: VendorNVIDIA,
	Targets: []Target{{
		Identify: identifyVolcanoVGPU,
		Metrics: []Metric{
			{Source: "Device_utilization_desc_of_container", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
			// Both native values are already bytes.
			{Source: "vGPU_device_memory_usage_in_bytes", Canonical: "accelerator_memory_used_bytes", Factor: 1},
			// The limit the slice was allocated, not the capacity of the
			// physical GPU. Limits of several slices must never be summed.
			{Source: "vGPU_device_memory_limit_in_bytes", Canonical: "accelerator_memory_total_bytes", Factor: 1},
		},
	}},
}

// identifyVolcanoVGPU resolves the Volcano vGPU observation target. The physical
// GPU the slice is carved from is named in deviceuuid, the slice in vdeviceid.
func identifyVolcanoVGPU(raw map[string]string, source Source, inventory Inventory) Identity {
	parent := first(raw, "deviceuuid", "UUID", "uuid")
	identity := Identity{
		Node:           firstNonEmpty(first(raw, "node", "Hostname", "hostname"), source.Labels[LabelNode]),
		Model:          firstNonEmpty(first(raw, "model", "modelName", "model_name"), source.Labels[LabelModel]),
		DeviceType:     DeviceTypeGPU,
		DeviceMode:     DeviceModeVirtual,
		Vendor:         VendorNVIDIA,
		DeviceID:       first(raw, "vdeviceid", "device_id"),
		ParentDeviceID: parent,
	}
	// The vGPU families omit node and model; take them from the physical device
	// that DCGM reported for the same identifier.
	if info, ok := inventory[parent]; ok {
		identity.Node = firstNonEmpty(identity.Node, info.Node)
		identity.Model = firstNonEmpty(identity.Model, info.Model)
	}
	return identity
}
