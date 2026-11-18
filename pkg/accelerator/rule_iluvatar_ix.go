package accelerator

// Official IX Exporter 4.3.0 metric units and physical-chip labels:
// https://github.com/Deep-Spark/ix-exporter/blob/v4.3.0/etc/metrics.yaml
// https://github.com/Deep-Spark/ix-exporter/blob/v4.3.0/pkg/collector/gpu_collector.go
// With Kubernetes collection disabled, 4.3.0 exposes only gpu/name/uuid. Local
// endpoint discovery supplies the node, independently of vendor Pod labels.
// IX Device Plugin splitboard=true, without replicas, allocates the same chip
// UUID. Board aggregation and time-slicing aliases need separate relation rules:
// https://github.com/Deep-Spark/ix-device-plugin/blob/v4.3.0/pkg/gpuallocator/deviceset.go
var iluvatarIXRule = Rule{
	Name: RuleIluvatarIX, Vendor: VendorIluvatar,
	Targets: []Target{{
		Identify: identifyIluvatarGPU,
		Metrics: []Metric{
			{Source: "ix_gpu_utilization", Canonical: "accelerator_core_utilization_percentage", Factor: 1},
			// This is memory activity, not used/capacity. Shared derivation
			// calculates memory capacity utilization from the measured bytes.
			{Source: "ix_mem_utilization", Canonical: "accelerator_memory_bandwidth_utilization_percentage", Factor: 1},
			{Source: "ix_mem_total", Canonical: "accelerator_memory_total_bytes", Factor: bytesPerMiB},
			{Source: "ix_mem_used", Canonical: "accelerator_memory_used_bytes", Factor: bytesPerMiB},
			{Source: "ix_mem_free", Canonical: "accelerator_memory_free_bytes", Factor: bytesPerMiB},
			{Source: "ix_temperature", Canonical: "accelerator_temperature_celsius", Factor: 1},
			{Source: "ix_power_usage", Canonical: "accelerator_power_usage_watts", Factor: 1},
			// Volatile ECC status flags are not monotonically increasing
			// counters. Only observed failures become diagnostic presence.
			{
				Source: "ix_ecc_sbe_vol_status", Canonical: "accelerator_health_diagnostic_info", Emit: nonzero,
				Enums: map[string]string{"category": "ecc", "code": "volatile_single_bit", "severity": SeverityWarning},
			},
			{
				Source: "ix_ecc_dbe_vol_status", Canonical: "accelerator_health_diagnostic_info", Emit: nonzero,
				Enums: map[string]string{"category": "ecc", "code": "volatile_double_bit", "severity": SeverityCritical},
			},
		},
		NativeResources: []NativeResource{{
			Names: []string{"iluvatar.com/gpu"}, Mode: DeviceModePhysical,
			DeviceID: func(_ Environment, raw map[string]string) string { return raw["uuid"] },
		}},
	}},
}

func identifyIluvatarGPU(env Environment, raw map[string]string) Identity {
	return Identity{
		Node:       firstNonEmpty(env.Source.Labels[LabelNode], raw["node_name"]),
		DeviceID:   raw["uuid"],
		DeviceType: DeviceTypeGPU, DeviceMode: DeviceModePhysical, Vendor: VendorIluvatar,
		Model: iluvatarModel(firstNonEmpty(raw["name"], env.Source.Labels[LabelModel])),
		// Newer IX versions expose these static labels. 4.3.0 leaves them
		// absent; do not infer the driver from the exporter image version.
		DriverVersion: raw["driver"], SerialNumber: raw["serial"],
	}
}

// MRC-V100-0x1B is a driver-reported variant of the platform's MRC-V100
// model. Keep this explicit alias separate from unrelated Iluvatar products.
func iluvatarModel(name string) string {
	model := labelValue(name)
	if model == "mrc-v100-0x1b" {
		return "mrc-v100"
	}
	return model
}
