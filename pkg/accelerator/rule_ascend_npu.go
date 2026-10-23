package accelerator

// This file interprets the Huawei Ascend npu-exporter. One endpoint exposes both
// the physical NPU families (npu_chip_*) and the vNPU families (vnpu_pod_*), so
// a single Rule carries two observation target classes and the endpoint is
// scraped once. Configuring them as two sources, as the exporter used to, meant
// the same endpoint was scraped twice and every duplicate was discarded.

const bytesPerKiB = 1024

var ascendNPURule = Rule{
	Name:   RuleAscendNPU,
	Vendor: VendorHuawei,
	Targets: []Target{
		{
			Identify: identifyAscendNPU,
			Metrics: []Metric{
				{Source: "npu_chip_info_utilization", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
				{Source: "npu_chip_info_hbm_bandwidth_utilization", Canonical: "accelerator_memory_bandwidth_utilization_percentage", Factor: 1},
				// The vendor's own composite figure. It is not comparable with
				// another vendor's, and the contract forbids aggregating it.
				{Source: "npu_chip_info_overall_utilization", Canonical: "accelerator_overall_utilization_percentage", Factor: 1},

				// The native unit is MiB.
				{Source: "npu_chip_info_hbm_used_memory", Canonical: "accelerator_memory_used_bytes", Factor: bytesPerMiB},
				{Source: "npu_chip_info_hbm_total_memory", Canonical: "accelerator_memory_total_bytes", Factor: bytesPerMiB},

				{Source: "npu_chip_info_temperature", Canonical: "accelerator_temperature_celsius", Factor: 1},
				// npu_chip_info_power_usage carries the same reading as
				// npu_chip_info_power. Only one mapping may produce a given
				// metric for a given target, so the duplicate is dropped rather
				// than left to be silently resolved by mapping order.
				{Source: "npu_chip_info_power", Canonical: "accelerator_power_usage_watts", Factor: 1},
				{Source: "npu_chip_info_power_limit", Canonical: "accelerator_power_limit_watts", Factor: 1},

				{
					// TODO(real-machine): confirm the value convention of
					// npu_chip_info_health_status. The exporter previously
					// passed it through as 1 = healthy, which is what this
					// mapping assumes: only a zero reading is a diagnostic.
					Source: "npu_chip_info_health_status", Canonical: "accelerator_health_diagnostic_info", Emit: zeroValue,
					Enums: map[string]string{"category": "npu", "code": "health_status", "severity": SeverityCritical},
				},

				{Source: "npu_chip_info_hbm_ecc_total_single_bit_error_cnt", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "hbm_ecc_single_bit"}},
				{Source: "npu_chip_info_hbm_ecc_total_double_bit_error_cnt", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "hbm_ecc_double_bit"}},
				{Source: "npu_chip_roce_rx_err_pkt_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_rx_bad_packet"}},
				{Source: "npu_chip_roce_tx_err_pkt_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_tx_bad_packet"}},
				{Source: "npu_chip_roce_new_pkt_rty_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_packet_retry"}},
				{Source: "npu_chip_roce_unexpected_ack_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_unexpected_ack"}},
				{Source: "npu_chip_roce_out_of_order_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_out_of_order"}},
				{Source: "npu_chip_roce_verification_err_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_verification"}},
				{Source: "npu_chip_roce_qp_status_err_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "roce_qp_status"}},
				{Source: "npu_chip_mac_tx_bad_pkt_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "mac_tx_bad_packet"}},
				{Source: "npu_chip_mac_rx_bad_pkt_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "mac_rx_bad_packet"}},
				{Source: "npu_chip_mac_tx_bad_oct_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "mac_tx_bad_octets"}},
				{Source: "npu_chip_mac_rx_bad_oct_num", Canonical: "accelerator_errors_total", Factor: 1, Enums: map[string]string{"error_type": "mac_rx_bad_octets"}},
			},
			Derive: []Deriver{
				deriveHealthState("npu_chip_info_health_status"),
			},
			NativeResources: []NativeResource{
				{
					Names:        []string{"huawei.com/Ascend910"},
					Mode:         DeviceModePhysical,
					DeviceID:     ascendChipIndex,
					AllocationID: prefixAscendAllocationID("Ascend910"),
				},
				{
					Names:        []string{"huawei.com/Ascend910B"},
					Mode:         DeviceModePhysical,
					DeviceID:     ascendChipIndex,
					AllocationID: prefixAscendAllocationID("Ascend910B"),
				},
			},
		},
		{
			Identify: identifyAscendVNPU,
			Metrics: []Metric{
				{Source: "vnpu_pod_aicore_utilization", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
				// The native unit is KiB.
				{Source: "vnpu_pod_used_memory", Canonical: "accelerator_memory_used_bytes", Factor: bytesPerKiB},
				// For a virtual instance this is the limit, not capacity.
				{Source: "vnpu_pod_total_memory", Canonical: "accelerator_memory_total_bytes", Factor: bytesPerKiB},
			},
			// TODO(real-machine): a vNPU is allocated under a virtual resource
			// name rather than as a device, and the vnpu_pod_* families name the
			// vNPU in a way that does not match any allocation identifier. No
			// native resource is declared, so a vNPU produces device-layer series
			// but no relation-layer series until that identifier is known.
		},
	},
}

func ascendChipIndex(_ Environment, raw map[string]string) string {
	return first(raw, "id", "device")
}

func prefixAscendAllocationID(resource string) func(string) string {
	return func(deviceID string) string {
		return resource + "-" + deviceID
	}
}

// identifyAscendNPU resolves the physical Ascend NPU observation target.
//
// TODO(real-machine): confirm that vdie_id is a stable unique device
// identifier. The samples also carry a whole-chip index in id, which is a
// position rather than an identity and which the specification forbids as a
// device_id. This mapping therefore prefers vdie_id and only falls back to the
// index when nothing else is present, in which case contract validation reports
// the target as having a decimal device_id instead of hiding it.
func identifyAscendNPU(env Environment, raw map[string]string) Identity {
	return Identity{
		Node:       firstNonEmpty(first(raw, "node", "Hostname", "hostname"), env.Source.Labels[LabelNode]),
		Model:      firstNonEmpty(first(raw, "model", "modelName", "model_name"), env.Source.Labels[LabelModel]),
		DeviceType: DeviceTypeNPU,
		DeviceMode: DeviceModePhysical,
		Vendor:     VendorHuawei,
		DeviceID:   first(raw, "vdie_id", "device_id", "id", "device"),
	}
}

// identifyAscendVNPU resolves the Ascend vNPU observation target.
//
// TODO(real-machine): the vnpu_pod_* families name the vNPU in v_dev_id but
// carry no identifier for the physical NPU it is carved from, so
// parent_device_id stays empty and contract validation reports
// missing_parent_device_id for every vNPU target. Filling it needs an
// allocation source the exporter does not read yet.
func identifyAscendVNPU(env Environment, raw map[string]string) Identity {
	return Identity{
		Node:       firstNonEmpty(first(raw, "node", "Hostname", "hostname"), env.Source.Labels[LabelNode]),
		Model:      firstNonEmpty(first(raw, "model", "modelName", "model_name"), env.Source.Labels[LabelModel]),
		DeviceType: DeviceTypeNPU,
		DeviceMode: DeviceModeVirtual,
		Vendor:     VendorHuawei,
		DeviceID:   first(raw, "v_dev_id", "device_id"),
	}
}
