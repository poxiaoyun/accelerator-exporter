package accelerator

// This file interprets the NVIDIA DCGM exporter. A single metric family covers
// both the whole physical GPU and its MIG partitions: a sample carrying
// GPU_I_ID describes a partition, a sample without it describes the device.
//
// The three machine-checkable assumptions this file makes are marked TODO and
// must be confirmed against a real DCGM endpoint before the mappings are
// treated as stable.

const bytesPerMiB = 1024 * 1024

var nvidiaDCGMRule = Rule{
	Name:   RuleNvidiaDCGM,
	Vendor: VendorNVIDIA,
	Targets: []Target{{
		Identify: identifyNVIDIAGPU,
		Metrics: []Metric{
			// Utilization. DCGM reports these for both the device and a
			// partition, so both modes are in scope.
			{Source: "DCGM_FI_DEV_GPU_UTIL", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
			{Source: "DCGM_FI_DEV_MEM_COPY_UTIL", Canonical: "accelerator_memory_bandwidth_utilization_percentage", Factor: 1},

			// Memory. DCGM reports MiB, the contract is bytes.
			{Source: "DCGM_FI_DEV_FB_USED", Canonical: "accelerator_memory_used_bytes", Factor: bytesPerMiB},
			{Source: "DCGM_FI_DEV_FB_FREE", Canonical: "accelerator_memory_free_bytes", Factor: bytesPerMiB},
			// DCGM publishes no total. FB_USED and FB_FREE alone leave out
			// FB_RESERVED, which on an H100 is 1638 MiB, so a total derived from
			// them understates the capacity by about 2%. The contract's total is
			// the device capacity, so the reserved part belongs in it.
			//
			// TODO(real-machine): confirm FB_RESERVED is reported for MIG
			// partitions too and means the partition's own reservation there. If
			// it is not, those targets produce no total here and fall back to the
			// derived one, which is what the specification allows.
			{
				Canonical: "accelerator_memory_total_bytes", Factor: bytesPerMiB,
				Sum: []string{"DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_FREE", "DCGM_FI_DEV_FB_RESERVED"},
			},

			// Physical-device readings. The contract restricts these to
			// device_mode=physical, which is what stops a MIG partition from
			// being shown the parent device's temperature and power.
			{Source: "DCGM_FI_DEV_GPU_TEMP", Canonical: "accelerator_temperature_celsius", Factor: 1},
			{Source: "DCGM_FI_DEV_POWER_USAGE", Canonical: "accelerator_power_usage_watts", Factor: 1},
			{Source: "DCGM_FI_DEV_ENFORCED_POWER_LIMIT", Canonical: "accelerator_power_limit_watts", Factor: 1},

			// Health. Each failing check becomes one diagnostic; the health
			// state is fanned in from all diagnostics by deriveHealthState.
			{
				Source: "DCGM_EXP_GPU_HEALTH_STATUS", Canonical: "accelerator_health_diagnostic_info", Emit: nonzero,
				EnumsFrom: []EnumAlias{
					{Label: "category", Aliases: []string{"health_error_category"}},
					{Label: "code", Aliases: []string{"health_error_code"}},
					{Label: "severity", Aliases: []string{"health_error_severity"}, Map: dcgmSeverities},
				},
			},
			{
				Source: "DCGM_FI_DEV_XID_ERRORS", Canonical: "accelerator_health_diagnostic_info", Emit: nonzero,
				Enums: map[string]string{"category": "xid", "code": "xid_error", "severity": SeverityWarning},
			},
			{
				Source: "DCGM_FI_DEV_ROW_REMAP_FAILURE", Canonical: "accelerator_health_diagnostic_info", Emit: nonzero,
				Enums: map[string]string{"category": "row_remap", "code": "row_remap_failure", "severity": SeverityWarning},
			},

			// Throttling.
			{
				Source: "DCGM_EXP_CLOCK_EVENTS_COUNT", Canonical: "accelerator_throttle_reason_info",
				Encode: positiveAsOne, EnumsFrom: []EnumAlias{{Label: "reason", Aliases: []string{"clock_event"}}},
			},

			// Errors. Only a monotonically increasing native counter may be
			// mapped onto a _total metric.
			{
				Source: "DCGM_EXP_XID_ERRORS_TOTAL", Canonical: "accelerator_errors_total", Factor: 1,
				Enums: map[string]string{"error_type": "xid"}, EnumsFrom: []EnumAlias{{Label: "error_code", Aliases: []string{"xid"}}},
			},
			{Source: "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "ecc_volatile_single_bit"}},
			{Source: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "ecc_volatile_double_bit"}},
			{Source: "DCGM_FI_DEV_ECC_SBE_AGG_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "ecc_persistent_single_bit"}},
			{Source: "DCGM_FI_DEV_ECC_DBE_AGG_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "ecc_persistent_double_bit"}},
			{Source: "DCGM_FI_DEV_RETIRED_SBE", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "retired_page_single_bit"}},
			{Source: "DCGM_FI_DEV_RETIRED_DBE", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "retired_page_double_bit"}},
			{Source: "DCGM_FI_DEV_RETIRED_PENDING", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "retired_page_pending"}},
			{Source: "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "remapped_row_correctable"}},
			{Source: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "remapped_row_uncorrectable"}},
			{Source: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "pcie_replay"}},
			{Source: "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "nvlink_crc_flit"}},
			{Source: "DCGM_FI_DEV_NVLINK_CRC_DATA_ERROR_COUNT_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "nvlink_crc_data"}},
			{Source: "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "nvlink_replay"}},
			{Source: "DCGM_FI_DEV_NVLINK_RECOVERY_ERROR_COUNT_TOTAL", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "nvlink_recovery"}},
		},
		Derive: []Deriver{
			deriveHealthState("DCGM_EXP_GPU_HEALTH_STATUS", "DCGM_FI_DEV_XID_ERRORS", "DCGM_FI_DEV_ROW_REMAP_FAILURE"),
		},
		NativeResources: []NativeResource{{
			Names:    []string{"nvidia.com/gpu"},
			Mode:     DeviceModePhysical,
			DeviceID: func(raw map[string]string) string { return first(raw, "UUID", "uuid") },
		}},
		// TODO(real-machine): MIG partitions are allocated under their own
		// extended resource names and the device plugin reports MIG device
		// identifiers, which do not appear on the DCGM samples. Until that
		// identifier is confirmed, a MIG allocation resolves to no observation
		// target and produces no relation-layer series, rather than being
		// attributed to the parent GPU.
	}},
}

// dcgmSeverities translates DCGM's health severity vocabulary into the contract
// closed set.
//
// TODO(real-machine): confirm the exact DCGM severity vocabulary. The contract
// only accepts info, warning and critical, and a severity outside this table
// produces no label, so the whole diagnostic is dropped by contract validation
// rather than exposed with an unknown value.
var dcgmSeverities = map[string]string{
	"info":           SeverityInfo,
	"not_applicable": SeverityInfo,
	"none":           SeverityInfo,
	"unknown":        SeverityInfo,
	"warning":        SeverityWarning,
	"isolate":        SeverityCritical,
}

// identifyNVIDIAGPU resolves the DCGM observation target.
//
// TODO(real-machine): confirm that DCGM reports the parent GPU UUID in UUID on
// MIG series and the partition in GPU_I_ID. This mapping assumes it does, and
// composes device_id as <parent_device_id>.<instance>, which the specification
// allows only for a source that cannot give an instance identifier. If DCGM
// does expose a stable MIG instance UUID, device_id must take that value
// instead and the composition must be dropped.
func identifyNVIDIAGPU(raw map[string]string, source Source, _ Inventory) Identity {
	device := first(raw, "UUID", "uuid")
	if device == "" {
		// TODO(real-machine): confirm DCGM always labels samples with UUID.
		// A bare index is a position, not an identity, and contract validation
		// reports it as a decimal device_id rather than silently accepting it.
		device = first(raw, "device")
	}
	identity := Identity{
		Node:          firstNonEmpty(first(raw, "node", "Hostname", "hostname"), source.Labels[LabelNode]),
		Model:         firstNonEmpty(first(raw, "model", "modelName", "model_name"), source.Labels[LabelModel]),
		DeviceType:    DeviceTypeGPU,
		DeviceMode:    DeviceModePhysical,
		Vendor:        VendorNVIDIA,
		DeviceID:      device,
		DriverVersion: first(raw, "driver_version", "DCGM_FI_DRIVER_VERSION"),
		PCIBusID:      first(raw, "pci_bus_id", "PCIBusID"),
	}
	if instance := first(raw, "GPU_I_ID", "gpu_instance_id"); instance != "" {
		identity.DeviceMode = DeviceModePartition
		identity.ParentDeviceID = device
		identity.DeviceID = device + "." + instance
		identity.DeviceIndex = instance
	}
	// TODO(real-machine): confirm DCGM labels MIG series with GPU_I_PROFILE.
	// Without it a partition has no profile, and the inventory simply omits the
	// label instead of inventing a specification.
	identity.Profile = first(raw, "GPU_I_PROFILE", "gpu_instance_profile")
	return identity
}

// BuildInventory extracts physical GPU identity from successful source scrapes.
// Virtual instance samples inherit node and model from the physical device that
// hosts them, because their own metric families omit those labels.
func BuildInventory(inputs []InventoryInput) Inventory {
	inventory := Inventory{}
	for _, input := range inputs {
		if input.Source.Rule != RuleNvidiaDCGM {
			continue
		}
		for _, samples := range input.Families {
			for _, sample := range samples {
				uuid := first(sample.Labels, "UUID", "uuid")
				if uuid == "" {
					continue
				}
				current := inventory[uuid]
				current.Node = firstNonEmpty(current.Node, first(sample.Labels, "node", "Hostname", "hostname"), input.Source.Labels[LabelNode])
				current.Model = firstNonEmpty(current.Model, first(sample.Labels, "model", "modelName", "model_name"), input.Source.Labels[LabelModel])
				inventory[uuid] = current
			}
		}
	}
	return inventory
}
