// Package transform maps vendor metrics to the accelerator metric contract.
package transform

import (
	"sort"
	"strings"

	"xiaoshiai.cn/rune/internal/model"
)

const (
	bytesPerMiB = 1024 * 1024
	bytesPerKiB = 1024
)

type metricMapping struct {
	source       string
	target       string
	factor       float64
	value        func(float64) float64
	labels       map[string]string
	labelAliases map[string][]string
}

var mappings = map[string][]metricMapping{
	model.AcceleratorTypeGPU: {
		{source: "DCGM_FI_DEV_GPU_UTIL", target: "accelerator_core_utilization_percentage", factor: 1},
		{source: "DCGM_FI_DEV_MEM_COPY_UTIL", target: "accelerator_memory_bandwidth_utilization_percentage", factor: 1},
		{source: "DCGM_FI_DEV_FB_USED", target: "accelerator_memory_used_bytes", factor: bytesPerMiB},
		{source: "DCGM_FI_DEV_FB_FREE", target: "accelerator_memory_free_bytes", factor: bytesPerMiB},
		{source: "DCGM_FI_DEV_GPU_TEMP", target: "accelerator_temperature_celsius", factor: 1},
		{source: "DCGM_FI_DEV_POWER_USAGE", target: "accelerator_power_usage_watts", factor: 1},
		{source: "DCGM_FI_DEV_ENFORCED_POWER_LIMIT", target: "accelerator_power_limit_watts", factor: 1},
		{
			source: "DCGM_EXP_GPU_HEALTH_STATUS", target: "accelerator_health_status", value: zeroIsHealthy,
			labelAliases: map[string][]string{
				"health_watch":          {"health_watch"},
				"health_error_code":     {"health_error_code"},
				"health_error_severity": {"health_error_severity"},
				"health_error_category": {"health_error_category"},
			},
		},
		{source: "DCGM_FI_DEV_XID_ERRORS", target: "accelerator_health_status", value: zeroIsHealthy, labels: map[string]string{"health_watch": "XID"}},
		{source: "DCGM_FI_DEV_ROW_REMAP_FAILURE", target: "accelerator_health_status", value: zeroIsHealthy, labels: map[string]string{"health_watch": "ROW_REMAP"}},
		{
			source: "DCGM_EXP_XID_ERRORS_TOTAL", target: "accelerator_error_total_count", factor: 1,
			labels: map[string]string{"error_type": "xid"}, labelAliases: map[string][]string{"error_code": {"xid"}},
		},
		{
			source: "DCGM_EXP_CLOCK_EVENTS_COUNT", target: "accelerator_throttle_reason_info", value: positiveAsOne,
			labelAliases: map[string][]string{"reason": {"clock_event"}},
		},
		{source: "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "ecc_volatile_single_bit"}},
		{source: "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "ecc_volatile_double_bit"}},
		{source: "DCGM_FI_DEV_ECC_SBE_AGG_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "ecc_persistent_single_bit"}},
		{source: "DCGM_FI_DEV_ECC_DBE_AGG_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "ecc_persistent_double_bit"}},
		{source: "DCGM_FI_DEV_RETIRED_SBE", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "retired_page_single_bit"}},
		{source: "DCGM_FI_DEV_RETIRED_DBE", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "retired_page_double_bit"}},
		{source: "DCGM_FI_DEV_RETIRED_PENDING", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "retired_page_pending"}},
		{source: "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "remapped_row_correctable"}},
		{source: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "remapped_row_uncorrectable"}},
		{source: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "pcie_replay"}},
		{source: "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "nvlink_crc_flit"}},
		{source: "DCGM_FI_DEV_NVLINK_CRC_DATA_ERROR_COUNT_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "nvlink_crc_data"}},
		{source: "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "nvlink_replay"}},
		{source: "DCGM_FI_DEV_NVLINK_RECOVERY_ERROR_COUNT_TOTAL", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "nvlink_recovery"}},
	},
	model.AcceleratorTypeNPU: {
		{source: "npu_chip_info_utilization", target: "accelerator_core_utilization_percentage", factor: 1},
		{source: "npu_chip_info_hbm_bandwidth_utilization", target: "accelerator_memory_bandwidth_utilization_percentage", factor: 1},
		{source: "npu_chip_info_overall_utilization", target: "accelerator_overall_utilization_percentage", factor: 1},
		{source: "npu_chip_info_hbm_used_memory", target: "accelerator_memory_used_bytes", factor: bytesPerMiB},
		{source: "npu_chip_info_hbm_total_memory", target: "accelerator_memory_total_bytes", factor: bytesPerMiB},
		{source: "npu_chip_info_temperature", target: "accelerator_temperature_celsius", factor: 1},
		{source: "npu_chip_info_power", target: "accelerator_power_usage_watts", factor: 1},
		{source: "npu_chip_info_power_usage", target: "accelerator_power_usage_watts", factor: 1},
		{source: "npu_chip_info_power_limit", target: "accelerator_power_limit_watts", factor: 1},
		{source: "npu_chip_info_health_status", target: "accelerator_health_status", factor: 1},
		{source: "npu_chip_info_process_info_num", target: "accelerator_used_count", value: positiveAsOne},
		{source: "npu_chip_info_hbm_ecc_total_single_bit_error_cnt", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "hbm_ecc_single_bit"}},
		{source: "npu_chip_info_hbm_ecc_total_double_bit_error_cnt", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "hbm_ecc_double_bit"}},
		{source: "npu_chip_roce_rx_err_pkt_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_rx_bad_packet"}},
		{source: "npu_chip_roce_tx_err_pkt_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_tx_bad_packet"}},
		{source: "npu_chip_roce_new_pkt_rty_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_packet_retry"}},
		{source: "npu_chip_roce_unexpected_ack_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_unexpected_ack"}},
		{source: "npu_chip_roce_out_of_order_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_out_of_order"}},
		{source: "npu_chip_roce_verification_err_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_verification"}},
		{source: "npu_chip_roce_qp_status_err_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "roce_qp_status"}},
		{source: "npu_chip_mac_tx_bad_pkt_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "mac_tx_bad_packet"}},
		{source: "npu_chip_mac_rx_bad_pkt_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "mac_rx_bad_packet"}},
		{source: "npu_chip_mac_tx_bad_oct_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "mac_tx_bad_octets"}},
		{source: "npu_chip_mac_rx_bad_oct_num", target: "accelerator_error_total_count", factor: 1, labels: map[string]string{"error_type": "mac_rx_bad_octets"}},
	},
	model.AcceleratorTypeVGPU: {
		{source: "Device_utilization_desc_of_container", target: "accelerator_core_utilization_percentage", factor: 1},
		{source: "vGPU_device_memory_limit_in_bytes", target: "accelerator_memory_total_bytes", factor: 1},
		{source: "vGPU_device_memory_usage_in_bytes", target: "accelerator_memory_used_bytes", factor: 1},
	},
	model.AcceleratorTypeVNPU: {
		{source: "vnpu_pod_aicore_utilization", target: "accelerator_core_utilization_percentage", factor: 1},
		{source: "vnpu_pod_total_memory", target: "accelerator_memory_total_bytes", factor: bytesPerKiB},
		{source: "vnpu_pod_used_memory", target: "accelerator_memory_used_bytes", factor: bytesPerKiB},
	},
}

// DeviceInfo is physical-device metadata used to enrich virtual metrics.
type DeviceInfo struct {
	Node             string
	Model            string
	MemoryTotalBytes float64
}

// Inventory maps physical GPU UUIDs to their node and model.
type Inventory map[string]DeviceInfo

// Input is one successful source scrape used to build shared metadata.
type Input struct {
	Source   model.Source
	Families model.Families
}

// BuildInventory extracts physical GPU metadata from successful source scrapes.
func BuildInventory(inputs []Input) Inventory {
	type memoryValues struct {
		used float64
		free float64
	}
	inventory := Inventory{}
	memoryByDevice := map[string]memoryValues{}
	for _, input := range inputs {
		source := input.Source
		families := input.Families
		if source.AcceleratorType != model.AcceleratorTypeGPU {
			continue
		}
		for rawName, samples := range families {
			for _, sample := range samples {
				uuid := first(sample.Labels, "UUID", "uuid")
				if uuid == "" {
					continue
				}
				current := inventory[uuid]
				current.Node = firstNonEmpty(current.Node, first(sample.Labels, "node", "Hostname", "hostname"), source.Labels["node"])
				current.Model = firstNonEmpty(current.Model, first(sample.Labels, "model", "modelName", "model_name"), source.Labels["model"])
				inventory[uuid] = current
				memory := memoryByDevice[uuid]
				switch rawName {
				case "DCGM_FI_DEV_FB_USED":
					memory.used = max(memory.used, sample.Value)
				case "DCGM_FI_DEV_FB_FREE":
					memory.free = max(memory.free, sample.Value)
				}
				memoryByDevice[uuid] = memory
			}
		}
	}
	for device, memory := range memoryByDevice {
		if memory.used == 0 && memory.free == 0 {
			continue
		}
		current := inventory[device]
		current.MemoryTotalBytes = (memory.used + memory.free) * bytesPerMiB
		inventory[device] = current
	}
	return inventory
}

// Transform converts one source scrape to normalized accelerator samples.
// It returns the converted samples and the number dropped for missing identity.
func Transform(source model.Source, families model.Families, inventory Inventory) ([]model.Sample, int) {
	sourceMappings := mappings[source.AcceleratorType]
	var samples []model.Sample
	dropped := 0
	for _, name := range CanonicalMetricNames() {
		for _, raw := range families[name] {
			labels := normalizeCanonicalLabels(source, raw.Labels, inventory)
			if labels["node"] == "" || labels["device"] == "" {
				dropped++
				continue
			}
			if labels["model"] == "" {
				labels["model"] = "unknown"
			}
			samples = append(samples, model.Sample{Name: name, Labels: labels, Value: raw.Value})
		}
	}
	for _, mapping := range sourceMappings {
		for _, raw := range families[mapping.source] {
			labels := normalizeLabels(source, raw.Labels, inventory)
			if labels["node"] == "" || labels["device"] == "" {
				dropped++
				continue
			}
			if labels["model"] == "" {
				labels["model"] = "unknown"
			}
			for name, value := range mapping.labels {
				labels[name] = value
			}
			for name, aliases := range mapping.labelAliases {
				setIfPresent(labels, name, first(raw.Labels, aliases...))
			}
			value := raw.Value * mapping.factor
			if mapping.value != nil {
				value = mapping.value(raw.Value)
			}
			samples = append(samples, model.Sample{Name: mapping.target, Labels: labels, Value: value})
		}
	}
	samples = append(samples, deriveMemoryMetrics(samples)...)
	samples = append(samples, deriveInfoMetrics(samples)...)
	samples = append(samples, deriveCountMetrics(samples)...)
	samples = append(samples, deriveShareMetrics(samples, inventory)...)
	sort.SliceStable(samples, func(i, j int) bool { return sampleKey(samples[i]) < sampleKey(samples[j]) })
	return samples, dropped
}

func zeroIsHealthy(value float64) float64 {
	if value == 0 {
		return 1
	}
	return 0
}

func positiveAsOne(value float64) float64 {
	if value > 0 {
		return 1
	}
	return 0
}

func normalizeLabels(source model.Source, raw map[string]string, inventory Inventory) map[string]string {
	labels := make(map[string]string, len(source.Labels)+10)
	for name, value := range source.Labels {
		if value != "" && name != "accelerator_type" {
			labels[name] = value
		}
	}
	labels["accelerator_type"] = source.AcceleratorType
	if labels["vendor"] == "" {
		switch source.AcceleratorType {
		case model.AcceleratorTypeGPU, model.AcceleratorTypeVGPU:
			labels["vendor"] = "nvidia"
		case model.AcceleratorTypeNPU, model.AcceleratorTypeVNPU:
			labels["vendor"] = "huawei"
		}
	}

	node := first(raw, "node", "Hostname", "hostname")
	modelName := first(raw, "model", "modelName", "model_name")
	device := first(raw, "device")
	switch source.AcceleratorType {
	case model.AcceleratorTypeGPU:
		device = first(raw, "UUID", "uuid", "device")
		if uuid := first(raw, "UUID", "uuid"); uuid != "" {
			labels["uuid"] = uuid
		}
		if partition := first(raw, "GPU_I_ID", "gpu_instance_id", "partition"); partition != "" {
			labels["partition"] = partition
		}
	case model.AcceleratorTypeNPU:
		device = first(raw, "id", "device", "vdie_id")
		if uuid := first(raw, "vdie_id"); uuid != "" {
			labels["uuid"] = uuid
		}
	case model.AcceleratorTypeVGPU:
		device = first(raw, "deviceuuid", "UUID", "uuid", "device")
		if info, ok := inventory[device]; ok {
			node = firstNonEmpty(node, info.Node)
			modelName = firstNonEmpty(modelName, info.Model)
		}
		if partition := first(raw, "vdeviceid", "partition"); partition != "" {
			labels["partition"] = partition
		}
	case model.AcceleratorTypeVNPU:
		device = first(raw, "v_dev_id", "device", "id")
		if partition := first(raw, "v_dev_id", "partition"); partition != "" {
			labels["partition"] = partition
		}
	}
	setIfPresent(labels, "node", firstNonEmpty(node, labels["node"]))
	setIfPresent(labels, "device", firstNonEmpty(device, labels["device"]))
	setIfPresent(labels, "model", firstNonEmpty(modelName, labels["model"]))
	setIfPresent(labels, "namespace", first(raw, "namespace", "podnamespace", "pod_namespace", "exported_namespace"))
	setIfPresent(labels, "pod", first(raw, "pod", "podname", "pod_name"))
	setIfPresent(labels, "container", first(raw, "container", "ctrname", "container_name"))
	setIfPresent(labels, "instance", first(raw, "app_instance", "rune_instance"))
	setIfPresent(labels, "driver_version", first(raw, "driver_version", "DCGM_FI_DRIVER_VERSION"))
	return labels
}

func normalizeCanonicalLabels(source model.Source, raw map[string]string, inventory Inventory) map[string]string {
	labels := cloneLabels(raw)
	for _, name := range []string{
		"Hostname", "hostname", "UUID", "modelName", "model_name", "id", "vdie_id",
		"deviceuuid", "vdeviceid", "v_dev_id", "GPU_I_ID", "gpu_instance_id",
		"podnamespace", "pod_namespace", "podname", "pod_name", "container_name", "ctrname", "exported_namespace",
		"app_instance", "rune_instance", "DCGM_FI_DRIVER_VERSION",
	} {
		delete(labels, name)
	}
	for name, value := range normalizeLabels(source, raw, inventory) {
		labels[name] = value
	}
	return labels
}

func deriveMemoryMetrics(samples []model.Sample) []model.Sample {
	type values struct {
		labels map[string]string
		used   *float64
		total  *float64
		free   *float64
	}
	byIdentity := map[string]*values{}
	existing := sampleKeys(samples)
	for index := range samples {
		sample := &samples[index]
		if sample.Name != "accelerator_memory_used_bytes" && sample.Name != "accelerator_memory_total_bytes" && sample.Name != "accelerator_memory_free_bytes" {
			continue
		}
		key := labelsKey(sample.Labels)
		current := byIdentity[key]
		if current == nil {
			current = &values{labels: sample.Labels}
			byIdentity[key] = current
		}
		switch sample.Name {
		case "accelerator_memory_used_bytes":
			current.used = &sample.Value
		case "accelerator_memory_total_bytes":
			current.total = &sample.Value
		case "accelerator_memory_free_bytes":
			current.free = &sample.Value
		}
	}
	var derived []model.Sample
	for _, current := range byIdentity {
		if current.total == nil && current.used != nil && current.free != nil {
			value := *current.used + *current.free
			current.total = &value
			sample := model.Sample{Name: "accelerator_memory_total_bytes", Labels: cloneLabels(current.labels), Value: value}
			if _, ok := existing[sampleKey(sample)]; !ok {
				derived = append(derived, sample)
				existing[sampleKey(sample)] = struct{}{}
			}
		}
		if current.free == nil && current.total != nil && current.used != nil {
			value := *current.total - *current.used
			if value < 0 {
				value = 0
			}
			sample := model.Sample{Name: "accelerator_memory_free_bytes", Labels: cloneLabels(current.labels), Value: value}
			if _, ok := existing[sampleKey(sample)]; !ok {
				derived = append(derived, sample)
				existing[sampleKey(sample)] = struct{}{}
			}
		}
		if current.used != nil && current.total != nil && *current.total > 0 {
			sample := model.Sample{
				Name: "accelerator_memory_utilization_percentage", Labels: cloneLabels(current.labels), Value: *current.used / *current.total * 100,
			}
			if _, ok := existing[sampleKey(sample)]; !ok {
				derived = append(derived, sample)
				existing[sampleKey(sample)] = struct{}{}
			}
		}
	}
	return derived
}

func deriveInfoMetrics(samples []model.Sample) []model.Sample {
	byDevice := map[string]model.Sample{}
	existingIdentities := map[string]struct{}{}
	for _, sample := range samples {
		if sample.Name == "accelerator_info" {
			existingIdentities[labelsKey(deviceLabels(sample.Labels))] = struct{}{}
		}
	}
	for _, sample := range samples {
		labels := deviceLabels(sample.Labels)
		info := model.Sample{Name: "accelerator_info", Labels: labels, Value: 1}
		identity := labelsKey(labels)
		if _, ok := existingIdentities[identity]; !ok {
			byDevice[identity] = info
		}
	}
	result := make([]model.Sample, 0, len(byDevice))
	for _, sample := range byDevice {
		result = append(result, sample)
	}
	return result
}

func deriveCountMetrics(samples []model.Sample) []model.Sample {
	existing := map[string]map[string]struct{}{
		"accelerator_total_count":           {},
		"accelerator_used_count":            {},
		"accelerator_partition_total_count": {},
	}
	for _, sample := range samples {
		identities := existing[sample.Name]
		if identities == nil {
			continue
		}
		identities[metricIdentityKey(sample.Labels, sample.Name == "accelerator_used_count")] = struct{}{}
	}

	total := map[string]model.Sample{}
	used := map[string]model.Sample{}
	partitions := map[string]model.Sample{}
	for _, sample := range samples {
		labels := deviceLabels(sample.Labels)
		identity := labelsKey(labels)
		if _, ok := existing["accelerator_total_count"][identity]; !ok {
			total[identity] = model.Sample{Name: "accelerator_total_count", Labels: labels, Value: 1}
		}
		if labels["partition"] != "" {
			if _, ok := existing["accelerator_partition_total_count"][identity]; !ok {
				partitions[identity] = model.Sample{Name: "accelerator_partition_total_count", Labels: cloneLabels(labels), Value: 1}
			}
		}
		if !hasWorkloadLabels(sample.Labels) {
			continue
		}
		workloadLabels := deviceAndWorkloadLabels(sample.Labels)
		workloadIdentity := labelsKey(workloadLabels)
		if _, ok := existing["accelerator_used_count"][workloadIdentity]; !ok {
			used[workloadIdentity] = model.Sample{Name: "accelerator_used_count", Labels: workloadLabels, Value: 1}
		}
	}

	result := make([]model.Sample, 0, len(total)+len(used)+len(partitions))
	for _, group := range []map[string]model.Sample{total, used, partitions} {
		for _, sample := range group {
			result = append(result, sample)
		}
	}
	return result
}

func deriveShareMetrics(samples []model.Sample, inventory Inventory) []model.Sample {
	existing := map[string]struct{}{}
	for _, sample := range samples {
		if sample.Name == "accelerator_share_percentage" {
			existing[metricIdentityKey(sample.Labels, true)] = struct{}{}
		}
	}
	derived := map[string]model.Sample{}
	for _, sample := range samples {
		if sample.Name != "accelerator_memory_total_bytes" || sample.Labels["accelerator_type"] != model.AcceleratorTypeVGPU {
			continue
		}
		physical, ok := inventory[sample.Labels["device"]]
		if !ok || physical.MemoryTotalBytes <= 0 {
			continue
		}
		labels := deviceAndWorkloadLabels(sample.Labels)
		identity := labelsKey(labels)
		if _, ok := existing[identity]; ok {
			continue
		}
		labels["share_basis"] = "memory"
		derived[identity] = model.Sample{
			Name:   "accelerator_share_percentage",
			Labels: labels,
			Value:  sample.Value / physical.MemoryTotalBytes * 100,
		}
	}
	result := make([]model.Sample, 0, len(derived))
	for _, sample := range derived {
		result = append(result, sample)
	}
	return result
}

func deviceLabels(labels map[string]string) map[string]string {
	result := map[string]string{}
	for _, name := range []string{"node", "device", "model", "partition", "uuid", "vendor", "accelerator_type", "driver_version"} {
		setIfPresent(result, name, labels[name])
	}
	return result
}

func deviceAndWorkloadLabels(labels map[string]string) map[string]string {
	result := deviceLabels(labels)
	for _, name := range []string{"namespace", "pod", "container", "instance"} {
		setIfPresent(result, name, labels[name])
	}
	return result
}

func metricIdentityKey(labels map[string]string, workload bool) string {
	if workload {
		return labelsKey(deviceAndWorkloadLabels(labels))
	}
	return labelsKey(deviceLabels(labels))
}

func hasWorkloadLabels(labels map[string]string) bool {
	return labels["namespace"] != "" || labels["pod"] != "" || labels["container"] != ""
}

func sampleKeys(samples []model.Sample) map[string]struct{} {
	keys := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		keys[sampleKey(sample)] = struct{}{}
	}
	return keys
}

func first(labels map[string]string, names ...string) string {
	for _, name := range names {
		if value := labels[name]; value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func setIfPresent(labels map[string]string, name, value string) {
	if value != "" {
		labels[name] = value
	}
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for name, value := range labels {
		cloned[name] = value
	}
	return cloned
}

func sampleKey(sample model.Sample) string {
	return sample.Name + "\xff" + labelsKey(sample.Labels)
}

func labelsKey(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for name, value := range labels {
		parts = append(parts, name+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\xff")
}
